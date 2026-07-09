# Runbook: shared Temporal on Rancher Desktop

Stand up a shared Temporal cluster on a local Rancher Desktop Kubernetes cluster,
with two teams, sample workflows, and JWT role-based access control (RBAC).
Follow it top to bottom; each layer builds on the previous one.

At the end you have: Temporal + Web UI on Kubernetes backed by in-cluster
PostgreSQL, two team namespaces each running their own workflow, and RBAC where
any member reads everything but only modifies their own team's workflows.

## Prerequisites

- **Rancher Desktop** with the Kubernetes cluster enabled (verified on k3s
  v1.35). Confirm the context exists: `kubectl config get-contexts | grep rancher-desktop`.
- **Helm** v3.8+ (verified on v4.0).
- **Go** 1.24+ (verified on 1.26) — builds the workers.
- **Temporal CLI** (`temporal`) — `brew install temporal`.
- Give the Rancher VM at least ~4 GB RAM (Rancher Desktop → Preferences).

Every `kubectl` command below passes `--context rancher-desktop`. If that is
already your current context, you can drop the flag. Run all commands from the
repo root.

---

## Layer 1 — namespace and PostgreSQL

PostgreSQL is the foundation: it holds all Temporal state. Create the Kubernetes
namespace, a throwaway DB password (created at runtime — never committed), then
the database.

```bash
kubectl --context rancher-desktop apply -f deploy/local/00-namespace.yaml

kubectl --context rancher-desktop -n temporal create secret generic temporal-db \
  --from-literal=password=temporal-local-dev

kubectl --context rancher-desktop apply -f deploy/local/10-postgres.yaml
kubectl --context rancher-desktop -n temporal rollout status statefulset/postgres
```

Verify both databases exist (`temporal` core + `temporal_visibility` search index):

```bash
kubectl --context rancher-desktop -n temporal exec postgres-0 -- \
  psql -U temporal -d temporal -tAc \
  "SELECT datname FROM pg_database WHERE datname LIKE 'temporal%' ORDER BY 1;"
# -> temporal
#    temporal_visibility
```

---

## Layer 2 — the Temporal server (Helm)

Add the chart repo and install the server pointed at that PostgreSQL. The chart
runs the schema migrations automatically (as Helm hook Jobs).

```bash
helm repo add temporal https://go.temporal.io/helm-charts
helm repo update temporal

helm install temporal temporal/temporal -n temporal \
  --kube-context rancher-desktop \
  -f deploy/local/20-temporal-values.yaml
```

Wait for all seven Deployments to become ready (~1 minute), then check cluster
health:

```bash
kubectl --context rancher-desktop -n temporal get deploy

# In a SEPARATE terminal, keep a port-forward to the frontend open:
kubectl --context rancher-desktop -n temporal port-forward svc/temporal-frontend 7233:7233

# Back in the first terminal:
temporal operator cluster health --address 127.0.0.1:7233   # -> SERVING
```

Key choices baked into `20-temporal-values.yaml`: `numHistoryShards: 512`
(immutable after this point), PostgreSQL advanced visibility (no Elasticsearch),
and `internal-frontend` enabled so internal traffic bypasses the authorizer we
add in Layer 6.

---

## Layer 3 — the team namespaces

Each team gets its own Temporal namespace (isolation of workflows, task queues,
and — once RBAC is on — access). With the port-forward from Layer 2 running:

```bash
temporal operator namespace create --address 127.0.0.1:7233 --retention 72h compute-provisioning
temporal operator namespace create --address 127.0.0.1:7233 --retention 72h team-b
temporal operator namespace list --address 127.0.0.1:7233 | grep Name
```

---

## Layer 4 — workers and workflows

Each team owns a worker that executes its workflow code. Build both and run them
as host processes that connect through the port-forward. (In production these are
Kubernetes Deployments — see [`workers/Dockerfile`](../workers/Dockerfile) and
[`deploy/gcp/`](../deploy/gcp/).)

```bash
cd workers
go build -o bin/compute-provisioning ./compute-provisioning
go build -o bin/team-b ./team-b
cd ..

# Run each worker in its own terminal (or background them):
TEMPORAL_ADDRESS=127.0.0.1:7233 TEMPORAL_NAMESPACE=compute-provisioning ./workers/bin/compute-provisioning
TEMPORAL_ADDRESS=127.0.0.1:7233 TEMPORAL_NAMESPACE=team-b ./workers/bin/team-b
```

Each logs `Started Worker` on its task queue (`provisioning-tq`, `orders-tq`).

Start a few workflow instances:

```bash
# compute-provisioning — provisioning pipeline
temporal workflow start --address 127.0.0.1:7233 -n compute-provisioning \
  --task-queue provisioning-tq --type ProvisionClusterWorkflow \
  --workflow-id prov-edge-01 --input '{"clusterName":"edge-01","nodeCount":3}'

# team-b — order processing
temporal workflow start --address 127.0.0.1:7233 -n team-b \
  --task-queue orders-tq --type OrderWorkflow \
  --workflow-id order-1001 --input '{"orderId":"ORD-1001","amount":42.50}'
```

Confirm they complete:

```bash
temporal workflow list --address 127.0.0.1:7233 -n compute-provisioning
temporal workflow list --address 127.0.0.1:7233 -n team-b
```

The provisioning workflow's `InstallOS` activity fails on its first attempt and
succeeds on the second — a deliberate retry you can see in the event history
(`temporal workflow show -n compute-provisioning --workflow-id prov-edge-01`).

---

## Layer 5 — the Web UI

```bash
kubectl --context rancher-desktop -n temporal port-forward svc/temporal-web 8080:8080
```

Open <http://localhost:8080>. Switch namespaces (top-left) between **compute-provisioning** and
**team-b** to see each team's runs. Click a workflow to inspect its event history,
inputs/outputs, and the `InstallOS` retry.

---

## Layer 6 — turn on JWT RBAC

Goal: every member can *read* all namespaces; a member can *modify* only their
own team; only an admin can *delete*. Temporal's built-in authorizer enforces
this from a `permissions` claim in each caller's JWT — no custom code, no server
rebuild.

### 6a. Generate the signing key, JWKS, and tokens

```bash
go run ./auth/tokengen -out ./auth/out
```

This writes an RSA key, `auth/out/jwks.json` (the public keys the server
validates against), and one token per identity under `auth/out/tokens/`. All
gitignored.

### 6b. Publish the JWKS in-cluster

The Temporal frontend fetches the JWKS over HTTP. A tiny nginx serves it:

```bash
kubectl --context rancher-desktop -n temporal create configmap jwks \
  --from-file=jwks.json=auth/out/jwks.json
kubectl --context rancher-desktop apply -f deploy/local/auth/30-jwks-server.yaml
kubectl --context rancher-desktop -n temporal rollout status deploy/temporal-jwks
```

### 6c. Enable the authorizer

Upgrade with the auth overlay layered on top of the base values:

```bash
helm upgrade temporal temporal/temporal -n temporal --kube-context rancher-desktop \
  -f deploy/local/20-temporal-values.yaml \
  -f deploy/local/21-temporal-values-auth.yaml
kubectl --context rancher-desktop -n temporal rollout status deploy/temporal-frontend
```

The frontend now rejects unauthenticated calls. Restart the port-forward if it
dropped during the rollout.

### 6d. Restart the workers with their tokens

Workers now need a `team-x:write` token to poll. Stop the tokenless workers from
Layer 4 and restart them with tokens:

```bash
TEMPORAL_ADDRESS=127.0.0.1:7233 TEMPORAL_NAMESPACE=compute-provisioning \
  TEMPORAL_AUTH_TOKEN=$(cat auth/out/tokens/worker-compute-provisioning.jwt) ./workers/bin/compute-provisioning

TEMPORAL_ADDRESS=127.0.0.1:7233 TEMPORAL_NAMESPACE=team-b \
  TEMPORAL_AUTH_TOKEN=$(cat auth/out/tokens/worker-team-b.jwt) ./workers/bin/team-b
```

### 6e. Verify the policy

Pass each user's token as gRPC metadata. `alice` is on compute-provisioning.

```bash
cd auth/out/tokens
A=127.0.0.1:7233

# No token -> denied
temporal workflow list --address $A -n compute-provisioning
# Error: ... Request unauthorized.

# alice READS team-b (read-all) -> allowed
temporal workflow list --address $A -n team-b \
  --grpc-meta "authorization=Bearer $(cat alice.jwt)"

# alice MODIFIES her own compute-provisioning -> allowed
temporal workflow start --address $A -n compute-provisioning --task-queue provisioning-tq \
  --type ProvisionClusterWorkflow --workflow-id alice-owns \
  --input '{"clusterName":"alice-edge","nodeCount":1}' \
  --grpc-meta "authorization=Bearer $(cat alice.jwt)"

# alice MODIFIES team-b (not her team) -> denied
temporal workflow start --address $A -n team-b --task-queue orders-tq \
  --type OrderWorkflow --workflow-id alice-forbidden --input '{"orderId":"X","amount":1}' \
  --grpc-meta "authorization=Bearer $(cat alice.jwt)"
# Error: ... Request unauthorized.
cd ../../..
```

### The identities

| Token | Member of | `permissions` claim | Can do |
|---|---|---|---|
| `alice`, `bob` | compute-provisioning | `temporal-system:read`, `compute-provisioning:write` | Read all; modify compute-provisioning |
| `carol`, `dave` | team-b | `temporal-system:read`, `team-b:write` | Read all; modify team-b |
| `admin` | — | `temporal-system:admin` | Everything, incl. delete |
| `worker-compute-provisioning` / `-b` | — | `compute-provisioning:write` / `team-b:write` | Poll + execute for that team |

**On the UI under RBAC:** the Web UI is pointed at the `internal-frontend`
(which bypasses auth) so it keeps full visibility on a laptop without an OIDC
login. In production the UI authenticates users via OIDC and talks to the
authorized frontend instead — see [`deploy/gcp/`](../deploy/gcp/).

**On delete:** Temporal classifies `DeleteWorkflowExecution` as a *write*, so a
`:write` token could delete its own team's workflows. Reserving delete strictly
for admins needs a small custom authorizer (or an edge rule) — out of scope for
this local setup; see the research repo's `multi-tenancy-setup.md`.

---

## Metrics and dashboards (optional)

To add Prometheus + Grafana with the two official Temporal dashboards (server
health + per-team SDK metrics), follow [`observability.md`](observability.md).
In short: install kube-prometheus-stack, enable the Temporal server
ServiceMonitor, run the workers in-cluster (`deploy/local/40-workers.yaml`), and
load the dashboard ConfigMaps.

## Teardown

```bash
helm uninstall temporal -n temporal --kube-context rancher-desktop
kubectl --context rancher-desktop delete namespace temporal
# if you installed the monitoring stack:
helm uninstall monitoring -n monitoring --kube-context rancher-desktop
kubectl --context rancher-desktop delete namespace monitoring
```

The PostgreSQL PersistentVolumeClaim is removed with the namespace, so this is a
clean reset.

---

## What was verified

Every command above was run on Rancher Desktop (k3s v1.35, Temporal server
v1.31.1, chart 1.5.0): 7 workflows across the two namespaces completed, the
`InstallOS` retry appears in history, the UI lists both teams, and the RBAC
checks in 6e returned exactly the allowed/denied results shown. The in-cluster
worker image (`workers/Dockerfile`) builds; the local runbook runs workers as
host processes for simplicity.
