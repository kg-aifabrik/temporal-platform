# temporal-platform

A shared, self-hosted [Temporal](https://temporal.io) workflow engine that
multiple engineering teams run their workflows on, with each team isolated in
its own Temporal namespace. This repo holds the deployment configs, sample
workers, and the auth tooling.

Two environments share the same design:

- **Local** — Temporal on a [Rancher Desktop](https://rancherdesktop.io)
  Kubernetes cluster with an in-cluster PostgreSQL. This is what
  [`docs/runbook-local-rancher-desktop.md`](docs/runbook-local-rancher-desktop.md) walks you through, end to end, in about 15 minutes.
- **Production (GCP)** — the same chart and topology on Google Kubernetes Engine
  (GKE) with Cloud SQL for PostgreSQL. See [`deploy/gcp/`](deploy/gcp/) for the
  differences. The design rationale lives in the companion research repo
  (`research/temporal`).

## What you get when you follow the runbook

- Temporal server (frontend, history, matching, worker, internal-frontend) + Web
  UI on Kubernetes, backed by PostgreSQL — no Elasticsearch.
- Two teams, each in its own namespace with its own task queue and workflow:
  - **team-a** — a mock bare-metal → OS (Rafay) → Kubernetes provisioning
    pipeline (5 activities, a retry).
  - **team-b** — a 5-step "order processing" toy workflow.
- **JWT role-based access control (RBAC)**, enforced live: any member can *read*
  every team's workflows, but can only *modify* their own team's; only an admin
  can delete. Four members (alice, bob → team-a; carol, dave → team-b) plus an
  admin.

## Layout

```
deploy/
  local/                     # Rancher Desktop
    00-namespace.yaml         # k8s namespace `temporal`
    10-postgres.yaml          # in-cluster PostgreSQL (StatefulSet)
    20-temporal-values.yaml   # Helm values — server + PostgreSQL, no auth
    21-temporal-values-auth.yaml  # overlay — turns on JWT RBAC
    40-workers.yaml           # in-cluster team workers + metrics Service + ServiceMonitor
    auth/
      30-jwks-server.yaml     # nginx serving the JWKS the frontend validates against
    monitoring/               # Prometheus + Grafana stack values + Temporal dashboards
  gcp/                        # production notes + what changes on GKE/Cloud SQL
auth/
  tokengen/                   # dependency-free Go tool: signing key + JWKS + tokens
  out/                        # generated key/JWKS/tokens (gitignored — never committed)
workers/
  team-a/                     # provisioning-pipeline worker + workflow (+ unit test)
  team-b/                     # order-processing worker + workflow (+ unit test)
  internal/temporalclient/    # shared client (attaches the bearer token)
  Dockerfile                  # in-cluster/production worker image
docs/
  runbook-local-rancher-desktop.md                  # platform operators: stand up the cluster locally
  writing-workflows.md        # team developers: write, run, test, debug a workflow
  activities-and-concurrency.md  # execution model (diagram) + concurrency/reliability scenarios
  observability.md            # Prometheus + Grafana + official Temporal dashboards
  test-plan.md                # what this repo tests, at which level, and how to run it
```

## Guides

- [`docs/runbook-local-rancher-desktop.md`](docs/runbook-local-rancher-desktop.md) — **platform operators**: stand up the whole cluster
  locally on Rancher Desktop, end to end.
- [`docs/writing-workflows.md`](docs/writing-workflows.md) — **team developers**:
  onboard a new team (team-c), then write, run, test, commit, and debug workflows;
  team-a is the mature reference example.
- [`docs/activities-and-concurrency.md`](docs/activities-and-concurrency.md) —
  **team developers, next level**: how work flows through Temporal (with a
  diagram), then three scenarios — a burst of requests (throttling), persisting
  state, and retrying on failure.
- [`docs/observability.md`](docs/observability.md) — **metrics + dashboards**:
  deploy Prometheus + Grafana with the official Temporal server and SDK
  dashboards (bundled locally, plug into existing monitoring on GKE).
- [`docs/test-plan.md`](docs/test-plan.md) — **what this repo tests**: unit,
  determinism/replay, end-to-end, RBAC, and smoke checks, at which level and how
  to run each.

## Quick start

Follow [`docs/runbook-local-rancher-desktop.md`](docs/runbook-local-rancher-desktop.md). In short: create the namespace and DB secret,
deploy PostgreSQL, install the Temporal Helm chart, create the two namespaces,
run the workers, start some workflows, then layer on RBAC.

> The password and all tokens in the local setup are throwaway, created at
> runtime and gitignored. Nothing secret is committed.
