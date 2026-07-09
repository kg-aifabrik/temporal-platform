# Runbook: shared Temporal on GKE + Cloud SQL

Stand up the shared Temporal platform on a hardened GKE cluster with Google
Cloud SQL for PostgreSQL as the state store, using **IAM database
authentication** (no stored password). The GKE cluster and the Cloud SQL
instance are built by the `iac-gke` cluster factory; Temporal is deployed on top
from this repo. Follow it top to bottom.

At the end you have: the `dev-fop` cluster running Temporal (server + Web UI)
against a private-IP Cloud SQL instance, workers from Artifact Registry, metrics
in Grafana, and workflows executing — then a clean teardown.

> **Status:** the `iac-gke` Cloud SQL enhancement is merged and validated
> (ADR-0010). The infra build and the Temporal deploy below are the procedure
> executed for the dev verification; the "Validated" results at the end are
> filled in from that run.

## Prerequisites

- **gcloud** authenticated (`gcloud auth login` + `gcloud auth application-default login`) with rights on the project, and **`gke-gcloud-auth-plugin`** installed (private cluster is reached over Connect Gateway).
- **kubectl**, **helm** v3.8+, **docker**, and the **temporal** CLI (`brew install temporal`).
- The **`iac-gke`** repo checked out (the cluster factory). Project **gke-poc-498602**, region **us-central1** (dev).
- Convenience vars used below:
  ```bash
  export PROJECT=gke-poc-498602 REGION=us-central1
  export GSA="temporal-sql@${PROJECT}.iam.gserviceaccount.com"
  export DB_IAM_USER="temporal-sql@${PROJECT}.iam"   # the GSA email minus ".gserviceaccount.com"
  ```

## Layer 1 — build the cluster + Cloud SQL (iac-gke pipeline)

Cloud SQL is opt-in per purpose; `fop` turns it on (`config/clusters.yaml`: `enable_cloud_sql: true`, ADR-0010). Apply only through the gated pipeline — never `terraform apply` from a laptop.

```bash
# one-time: the automation SA needs the two Cloud SQL build roles
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:cluster-ctrl-automation@${PROJECT}.iam.gserviceaccount.com" \
  --role=roles/cloudsql.admin --condition=None
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:cluster-ctrl-automation@${PROJECT}.iam.gserviceaccount.com" \
  --role=roles/servicenetworking.networksAdmin --condition=None

# build (from the iac-gke repo). foundation first (enables sqladmin + servicenetworking),
# then fop (VPC + PSA + cluster + Cloud SQL + Artifact Registry).
gh workflow run terraform-apply.yml -f env=dev -f purpose=foundation   # approve if the dev env gates
gh workflow run terraform-apply.yml -f env=dev -f purpose=fop
gh run watch <run-id> --exit-status
```

**Validate:**
```bash
gcloud container clusters list --project $PROJECT           # dev-fop RUNNING
gcloud sql instances list --project $PROJECT                # dev-fop-temporal-<suffix> RUNNABLE, PRIVATE IP
gcloud sql instances describe dev-fop-temporal-<suffix> --project $PROJECT \
  --format="value(settings.ipConfiguration.ipv4Enabled, settings.databaseFlags)"  # ipv4=false; iam_authentication=on
gcloud services list --enabled --project $PROJECT | grep -E "sqladmin|servicenetworking"
```

## Layer 2 — connect to the cluster

```bash
gcloud container clusters get-credentials dev-fop --region $REGION --project $PROJECT
kubectl get nodes    # 3 nodes Ready (reached over Connect Gateway)
```

## Layer 3 — Temporal identity + IAM database auth

The Temporal server authenticates to Cloud SQL as a Google service account via
Workload Identity — no password anywhere.

```bash
CONN=$(gcloud sql instances describe dev-fop-temporal-<suffix> --project $PROJECT --format='value(connectionName)')

# 1. a service account for Temporal's DB access
gcloud iam service-accounts create temporal-sql --project $PROJECT \
  --display-name="Temporal Cloud SQL (IAM DB auth)"
gcloud projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:${GSA}" --role=roles/cloudsql.client --condition=None
gcloud projects add-iam-policy-binding "$PROJECT" --member="serviceAccount:${GSA}" --role=roles/cloudsql.instanceUser --condition=None

# 2. the IAM database user on the instance
gcloud sql users create "$GSA" --instance=dev-fop-temporal-<suffix> --project $PROJECT \
  --type=cloud_iam_service_account

# 3. Workload Identity: the KSA "temporal" (namespace temporal) impersonates the GSA
kubectl create namespace temporal
gcloud iam service-accounts add-iam-policy-binding "$GSA" --project $PROJECT \
  --role=roles/iam.workloadIdentityUser \
  --member="serviceAccount:${PROJECT}.svc.id.goog[temporal/temporal]"
```

Both databases (`temporal`, `temporal_visibility`) already exist — the `cloud-sql`
module created them. The IAM user needs table privileges on them, granted during schema setup (next layer runs as this identity).

## Layer 4 — schema (one-off Job with the Auth Proxy)

The chart's schema hook can't carry the Cloud SQL Auth Proxy, so run schema
setup as a controlled one-off Job (`deploy/gcp/schema-job.yaml`) that pairs
`temporal-sql-tool` with the proxy sidecar and the `temporal` KSA. It sets up
and versions both databases.

```bash
sed -e "s|__CLOUD_SQL_CONNECTION_NAME__|${CONN}|" \
    -e "s|__TEMPORAL_DB_IAM_USER__|${DB_IAM_USER}|" \
    deploy/gcp/schema-job.yaml | kubectl -n temporal apply -f -
kubectl -n temporal wait --for=condition=complete job/temporal-schema --timeout=300s
kubectl -n temporal logs job/temporal-schema -c schema | tail -5   # "schema setup complete"
```

## Layer 5 — deploy Temporal

Fill the placeholders in [`deploy/gcp/gke-values.yaml`](../deploy/gcp/gke-values.yaml) and install:

```bash
sed -e "s|__TEMPORAL_GSA_EMAIL__|${GSA}|" \
    -e "s|__TEMPORAL_DB_IAM_USER__|${DB_IAM_USER}|" \
    -e "s|__CLOUD_SQL_CONNECTION_NAME__|${CONN}|" \
    deploy/gcp/gke-values.yaml > /tmp/gke-values.rendered.yaml

helm repo add temporal https://go.temporal.io/helm-charts && helm repo update temporal
helm install temporal temporal/temporal -n temporal -f /tmp/gke-values.rendered.yaml
kubectl -n temporal rollout status deploy/temporal-frontend

# team namespaces
kubectl -n temporal port-forward svc/temporal-frontend 7233:7233 &
temporal operator namespace create --address 127.0.0.1:7233 --retention 72h compute-provisioning
temporal operator namespace create --address 127.0.0.1:7233 --retention 72h team-b
temporal operator cluster health --address 127.0.0.1:7233   # SERVING
```

## Layer 6 — workers (from Artifact Registry)

```bash
gcloud auth configure-docker ${REGION}-docker.pkg.dev
REPO=${REGION}-docker.pkg.dev/${PROJECT}/app
docker build --build-arg TEAM=compute-provisioning -t $REPO/temporal-worker-compute-provisioning:dev workers/
docker build --build-arg TEAM=team-b -t $REPO/temporal-worker-team-b:dev workers/
docker push $REPO/temporal-worker-compute-provisioning:dev && docker push $REPO/temporal-worker-team-b:dev
# deploy the worker Deployments (TEMPORAL_ADDRESS=temporal-frontend.temporal.svc:7233; no token — RBAC off here)
sed "s|__AR_REPO__|${REPO}|" deploy/gcp/workers.yaml | kubectl -n temporal apply -f -
```

## Layer 7 — metrics and dashboards

Same stack as local (kube-prometheus-stack + the two Temporal dashboards). On a
real shared GKE you would instead point the cluster's existing Prometheus /
Google Managed Prometheus at the ServiceMonitors — see
[`observability.md`](observability.md).

```bash
helm install monitoring prometheus-community/kube-prometheus-stack -n monitoring --create-namespace \
  -f deploy/local/monitoring/kube-prometheus-stack-values.yaml
# server serviceMonitor is already on via gke-values.yaml; apply the worker ServiceMonitor + dashboards
# (reuse deploy/local/monitoring/dashboards/*.json as grafana_dashboard ConfigMaps)
```

## Layer 8 — verify end-to-end

```bash
A=127.0.0.1:7233
for i in $(seq 1 20); do
  temporal workflow start --address $A -n compute-provisioning --task-queue provisioning-tq \
    --type ProvisionClusterWorkflow --workflow-id gke-prov-$i --input '{"clusterName":"edge-'$i'","nodeCount":2}'
  temporal workflow start --address $A -n team-b --task-queue orders-tq \
    --type OrderWorkflow --workflow-id gke-ord-$i --input '{"orderId":"O-'$i'","amount":10}'
done
temporal workflow list --address $A -n compute-provisioning   # Completed
```

Checklist:
- [ ] `dev-fop` cluster RUNNING; Cloud SQL RUNNABLE with a **private IP** and `cloudsql.iam_authentication=on`.
- [ ] Temporal `SERVING`; schema present in both Cloud SQL databases.
- [ ] Workflows across both namespaces reach `Completed`.
- [ ] Grafana shows the server + SDK dashboards with live data.
- [ ] **No database password exists** anywhere — no k8s Secret, no `existingSecret`; the server connects via the Auth Proxy as the IAM user. (`kubectl -n temporal get secret` shows no DB password secret.)

## Teardown

```bash
helm uninstall temporal -n temporal
helm uninstall monitoring -n monitoring
kubectl delete namespace temporal monitoring
gcloud sql users delete "$GSA" --instance=dev-fop-temporal-<suffix> --project $PROJECT --quiet
gcloud iam service-accounts delete "$GSA" --project $PROJECT --quiet
# destroy the cluster + Cloud SQL via the pipeline
gh workflow run terraform-destroy.yml -f env=dev -f purpose=fop -f confirm=fop   # (from iac-gke)
```

## Validated (dev, gke-poc-498602)

*Filled in from the verification run once complete: cluster + Cloud SQL up,
IAM-auth connection working with no password, schema created, workflows
Completed across both namespaces, and both Grafana dashboards populated.*
