# temporal-platform

A shared, self-hosted [Temporal](https://temporal.io) workflow engine that
multiple engineering teams run their workflows on, with each team isolated in
its own Temporal namespace. This repo holds the deployment configs, sample
workers, and the auth tooling.

Two environments share the same design:

- **Local** — Temporal on a [Rancher Desktop](https://rancherdesktop.io)
  Kubernetes cluster with an in-cluster PostgreSQL. This is what
  [`RUNBOOK.md`](RUNBOOK.md) walks you through, end to end, in about 15 minutes.
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
    auth/
      30-jwks-server.yaml     # nginx serving the JWKS the frontend validates against
  gcp/                        # production notes + what changes on GKE/Cloud SQL
auth/
  tokengen/                   # dependency-free Go tool: signing key + JWKS + tokens
  out/                        # generated key/JWKS/tokens (gitignored — never committed)
workers/
  team-a/                     # provisioning-pipeline worker + workflow
  team-b/                     # order-processing worker + workflow
  internal/temporalclient/    # shared client (attaches the bearer token)
  Dockerfile                  # in-cluster/production worker image
RUNBOOK.md                    # step-by-step local setup
```

## Guides

- [`RUNBOOK.md`](RUNBOOK.md) — **platform operators**: stand up the whole cluster
  locally on Rancher Desktop, end to end.
- [`docs/writing-workflows.md`](docs/writing-workflows.md) — **team developers**:
  write, run, test, and debug your own workflow from scratch, using team-a as the
  worked example.

## Quick start

Follow [`RUNBOOK.md`](RUNBOOK.md). In short: create the namespace and DB secret,
deploy PostgreSQL, install the Temporal Helm chart, create the two namespaces,
run the workers, start some workflows, then layer on RBAC.

> The password and all tokens in the local setup are throwaway, created at
> runtime and gitignored. Nothing secret is committed.
