# temporal-platform

A shared, self-hosted [Temporal](https://temporal.io) workflow engine that
several engineering teams run their workflows on. The **platform team** owns and
operates the shared infrastructure; **each team** writes its own workflow logic
and talks to the platform as a client. This repo holds the deployment configs,
sample workers, a sample API service, and the tooling to run it all — on a laptop
or on Google Kubernetes Engine (GKE).

- **Shared infrastructure, owned by the platform team** — one Temporal cluster
  (server + Web UI + database), role-based access control, and monitoring. Teams
  don't run any of it; they use it.
- **Each team writes its own workflow logic** and interacts with the platform
  through a **Temporal Client**. Every team is isolated in its own namespace.
- **Sample workflows and a service to connect with** — `compute-provisioning`
  (a mock bare-metal → OS → Kubernetes pipeline) and `team-b` (a small
  order-processing toy), plus a **gRPC API service** (`examples/api-frontend`)
  that fronts a workflow so callers submit and track work over gRPC using a
  Temporal Client.
- **Other useful things** — live JWT role-based access control (read-all /
  write-own / admin-only-delete), Prometheus + Grafana dashboards, and runbooks
  that stand the whole thing up end to end.

![Shared Temporal Platform: your team writes workflows, activities, and tests and commits them; the platform's CI/CD builds and deploys your worker; you start and track workflows through a Temporal Client or the gRPC frontend; the platform team owns the shared server, Cloud SQL, RBAC, monitoring, and per-team namespaces](docs/diagrams/platform-overview.png)

## Understanding this repo

### Layout

```
runbooks/                    # stand up the platform (for operators)
  runbook-local-rancher-desktop.md   # local, end to end (~15 min)
  runbook-gke-cloud-sql.md           # GKE + Cloud SQL with IAM auth (validated)
docs/                        # how-to guides (for team developers)
  writing-workflows.md              # write, run, test, debug a workflow from zero
  activities-and-concurrency.md     # execution model + concurrency/reliability
  observability.md                  # Prometheus + Grafana + Temporal dashboards
  api-frontend-for-temporal.md      # build a gRPC frontend over a workflow
  test-plan.md                      # what this repo tests, and how
examples/
  api-frontend/              # gRPC + Proto service that fronts a workflow (Temporal Client)
workers/
  compute-provisioning/      # sample: provisioning pipeline (workflow + activities + test)
  team-b/                    # sample: order processing (workflow + test)
  internal/temporalclient/   # shared client — attaches the bearer token, wires metrics
deploy/
  local/                     # Rancher Desktop manifests + Helm values + monitoring
  gcp/                       # GKE + Cloud SQL Helm values + what changes in production
auth/
  tokengen/                  # dependency-free Go tool: signing key + JWKS + demo tokens
```

### Key concepts

Five ideas carry the whole platform. Once they click, the rest is detail.

- **Namespace** — a team's own space inside the one shared cluster. Its workflows,
  task queues, and access control all live there. Teams are isolated by namespace,
  not by separate clusters.
- **Workflow** — your orchestration code: what happens, and in what order. It is
  durable — if the process crashes, Temporal replays its history and continues —
  which is why workflow code must be deterministic (no clock, randomness, or
  network calls inside it).
- **Activity** — a plain function that does the real work (call an API, install an
  OS). Activities are allowed to fail, and Temporal retries them for you.
- **Worker** — the process that runs your workflow + activity code and polls a
  task queue. Your team owns the code; the platform's pipeline runs it in the
  shared environments.
- **Client** — how you start and query workflows from outside the cluster: a
  Temporal Client embedded in your own service, or the sample **gRPC API
  frontend** in `examples/api-frontend`.

The split in ownership: the **platform team** owns the cluster, the database,
RBAC, monitoring, and the CI/CD pipeline that deploys your worker. **Your team**
owns only the workflow code, activities, and tests.

## Writing your own workflows

Start here and read in order:

- **[docs/writing-workflows.md](docs/writing-workflows.md)** — the main guide:
  onboard a new team, then write, run, test, commit, and debug a workflow from
  zero. `compute-provisioning` is the reference example.
- **Understanding concurrency** —
  [docs/activities-and-concurrency.md](docs/activities-and-concurrency.md): how
  work flows through Temporal, then three scenarios — throttling a burst of
  requests, persisting progress mid-run, and retrying on failure.
- **Testing your workflows** —
  [docs/test-plan.md](docs/test-plan.md): what to test and at which level (unit,
  determinism/replay, end-to-end, RBAC), and how to run each.
- **Observability** —
  [docs/observability.md](docs/observability.md): deploy Prometheus + Grafana with
  the official Temporal server and SDK dashboards.
- **Exposing a workflow over an API** —
  [docs/api-frontend-for-temporal.md](docs/api-frontend-for-temporal.md): build a
  gRPC frontend that submits and tracks workflows, with the worked example in
  `examples/api-frontend`.

## Testing

- **Local, on Rancher Desktop** — the fastest way to see the whole platform run on
  your own machine: **[runbooks/runbook-local-rancher-desktop.md](runbooks/runbook-local-rancher-desktop.md)**.
  Two teams, sample workflows, live RBAC, and dashboards in about 15 minutes.
- **Production, on GKE + Cloud SQL** —
  [runbooks/runbook-gke-cloud-sql.md](runbooks/runbook-gke-cloud-sql.md): the same
  platform on GKE, backed by Cloud SQL with IAM database authentication (no stored
  password), validated end to end.

> The password and all tokens in the local setup are throwaway — created at
> runtime and gitignored. Nothing secret is committed.
