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

## For platform engineers

You own the shared infrastructure — the cluster, the database, access control,
and monitoring — and you grow it as more teams come aboard. Your path:

1. **Learn the shape locally.** Stand up the whole platform on your laptop in
   about 15 minutes:
   **[runbooks/runbook-local-rancher-desktop.md](runbooks/runbook-local-rancher-desktop.md)**
   — two teams, sample workflows, live RBAC, and dashboards.
2. **Deploy production.** The same platform on GKE + Cloud SQL with IAM database
   authentication (no stored password):
   **[runbooks/runbook-gke-cloud-sql.md](runbooks/runbook-gke-cloud-sql.md)**. The
   cluster and database it runs on are built by the companion
   [`iac-gke`](https://github.com/kg-aifabrik/iac-gke) repo; what changes from
   local is spelled out in **[deploy/gcp/README.md](deploy/gcp/README.md)**.
3. **Operate it.** Wire up metrics and the Temporal dashboards
   ([docs/observability.md](docs/observability.md)); onboard a team by creating its
   namespace and issuing tokens
   ([docs/writing-workflows.md#onboarding](docs/writing-workflows.md#onboarding)).
4. **Enhance it over time.** Add teams, set per-namespace rate limits, and weigh
   the auth and visibility trade-offs. The design rationale and the multi-tenancy
   gaps a platform team must build live in the research repo —
   `research/temporal/shared-instance-architecture.md` and
   `research/temporal/multi-tenancy-setup.md`.

## For workflow developers

You build your team's workflows and shepherd each one from your laptop to
production. You own the workflow code, activities, and tests; the platform runs
them. Read **Key concepts** above, then follow the main guide —
**[docs/writing-workflows.md](docs/writing-workflows.md)** — which takes a new team
from zero through the whole lifecycle:

| Stage | What you do | Where |
|---|---|---|
| **Develop** | write a workflow + activities, run a worker on your laptop | [writing-workflows § Your first workflow](docs/writing-workflows.md#your-first-workflow) |
| **Design for scale & reliability** | throttle concurrency, persist progress, handle retries | [activities-and-concurrency.md](docs/activities-and-concurrency.md) |
| **Test** | unit, activity, and replay tests | [writing-workflows § Writing tests](docs/writing-workflows.md#writing-tests) · [test-plan.md](docs/test-plan.md) |
| **Ship** | commit → the platform's CI/CD builds your worker and deploys it to staging | [writing-workflows § How your code ships](docs/writing-workflows.md#how-your-code-ships-to-shared-environments) |
| **Verify & promote** | watch it in the Web UI and Grafana in staging, then promote to production | [observability.md](docs/observability.md) |
| **Debug** | inspect history, query state, reset and replay | [writing-workflows § Debugging a workflow](docs/writing-workflows.md#debugging-a-workflow) |

Triggering workflows from another service or over an API?
**[docs/api-frontend-for-temporal.md](docs/api-frontend-for-temporal.md)** builds a
gRPC frontend that submits and tracks workflows with a Temporal Client (worked
example in `examples/api-frontend`).

> The password and all tokens in the local setup are throwaway — created at
> runtime and gitignored. Nothing secret is committed.
