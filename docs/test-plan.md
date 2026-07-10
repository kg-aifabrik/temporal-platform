# Test plan

What this repo tests, at which level, and how to run it. It follows the Temporal
testing pyramid: many fast unit tests on workflow logic, a determinism gate to
protect running workflows, and a small number of end-to-end and access-control
checks against a real cluster.

Status legend: ✅ automated · 🔶 manual (documented, reproducible) · ⬜ planned.

| # | Level | Covers | How | Status |
|---|---|---|---|---|
| 1 | Unit — workflow logic | Each team's workflow orchestrates its activities correctly | `go test ./...` (time-skipping env, mocked activities) | ✅ |
| 2 | Determinism / replay | A code change won't break workflows already running | `WorkflowReplayer` against saved histories, in CI | ⬜ |
| 3 | Integration / e2e | Deploy → start workflows → they complete; retry is recorded | [`runbook-local-rancher-desktop.md`](../runbooks/runbook-local-rancher-desktop.md) §4, or `scripts/e2e.sh` | 🔶 |
| 4 | Authorization (RBAC) | read-all / write-own / deny, per the policy | [`runbook-local-rancher-desktop.md`](../runbooks/runbook-local-rancher-desktop.md) §6e matrix | 🔶 |
| 5 | Infrastructure smoke | PostgreSQL + both DBs, cluster health, pods ready, JWKS reachable | commands below | 🔶 |

---

## 1. Unit tests — workflow logic ✅

The core, and the part every team owns. Each workflow has a test that runs in
the SDK's **time-skipping** environment with **mocked activities**: the durable
timers fire instantly, no Temporal server is needed, and the test finishes in
milliseconds. It asserts the workflow drives its activities in the right order
and returns the right result — not the activity bodies (those are mocked).

Files: [`workers/compute-provisioning/workflow_test.go`](../workers/compute-provisioning/workflow_test.go),
[`workers/team-b/workflow_test.go`](../workers/team-b/workflow_test.go).

```bash
cd workers && go test ./...
# ok  .../workers/compute-provisioning
# ok  .../workers/team-b
```

**Every new team's workflow must ship a test of this shape** — it is the template
in [`writing-workflows.md`](writing-workflows.md#test-before-you-deploy). Name each
test by the behavior it proves.

## 2. Determinism / replay tests ⬜ (planned)

The failure mode unique to Temporal: because a workflow's state is rebuilt by
replaying its history, an incompatible change to workflow code can wedge or
corrupt workflows that are **already running**. The guard is a replay test —
feed real (or captured) event histories to `WorkflowReplayer` against the current
code and fail if they diverge.

Planned wiring: capture a few histories per workflow type (`temporal workflow
show -o json`) into `workers/<team>/testdata/`, add a `TestReplay` that replays
them, and make it a required CI check on every pull request that touches workflow
code. Until then, run the manual e2e (below) after any workflow change.

## 3. Integration / end-to-end 🔶

Exercises the whole path on the real local cluster: the platform is deployed,
workers connect, workflows are started and run to completion, and the deliberate
`InstallOS` retry is recorded in history. Today this is the verification built
into the runbook; the intent is to lift it into a script (`scripts/e2e.sh`) that
a continuous-integration runner can execute against a disposable cluster.

Run it manually per [`runbook-local-rancher-desktop.md`](../runbooks/runbook-local-rancher-desktop.md) §2–4. The assertions that must hold:

```bash
A=127.0.0.1:7233
# both teams' workflows reach Completed
temporal workflow list --address $A -n compute-provisioning --query 'ExecutionStatus="Completed"'
temporal workflow list --address $A -n team-b --query 'ExecutionStatus="Completed"'
# the retry is visible: InstallOS ran a second attempt
temporal workflow show --address $A -n compute-provisioning --workflow-id prov-edge-01 | grep -i "attempt 2"
```

Expected: three compute-provisioning and (at least) one team-b execution Completed; `InstallOS`
shows attempt 2. This was run during setup and passed.

## 4. Authorization (RBAC) tests 🔶

Proves the access policy: any member reads every namespace, modifies only their
own, and unauthenticated calls are refused. Each case is one CLI call with a
user's token as gRPC metadata. Run from `auth/out/tokens/` after RBAC is enabled
(runbook §6). This matrix was executed during setup and returned exactly these
results.

| Case | Command shape (token) | Namespace / action | Expected |
|---|---|---|---|
| No token | *(none)* | compute-provisioning / list | ❌ `Request unauthorized` |
| Read across teams | `alice.jwt` | team-b / list | ✅ allowed |
| Write own team | `alice.jwt` | compute-provisioning / start | ✅ allowed |
| Write another team | `alice.jwt` | team-b / start | ❌ `Request unauthorized` |
| Admin | `admin.jwt` | any / any | ✅ allowed |
| Worker poll | `worker-compute-provisioning.jwt` | compute-provisioning / poll | ✅ allowed |

```bash
cd auth/out/tokens; A=127.0.0.1:7233
temporal workflow list  --address $A -n compute-provisioning                                            # -> unauthorized
temporal workflow list  --address $A -n team-b --grpc-meta "authorization=Bearer $(cat alice.jwt)"  # -> ok
temporal workflow start --address $A -n team-b --task-queue orders-tq --type OrderWorkflow \
  --workflow-id t --input '{"orderId":"X","amount":1}' \
  --grpc-meta "authorization=Bearer $(cat alice.jwt)"                                     # -> unauthorized
```

Note: reserving *delete* for admins only is not covered here — Temporal classifies
`DeleteWorkflowExecution` as a write, so it needs a custom authorizer (see the
research repo's `multi-tenancy-setup.md`). That is out of scope for this repo.

## 5. Infrastructure smoke checks 🔶

Fast confidence that the platform itself is healthy — run after deploy and after
any Helm change.

```bash
CTX="--context rancher-desktop"
# PostgreSQL up with both databases
kubectl $CTX -n temporal exec postgres-0 -- \
  psql -U temporal -tAc "SELECT count(*) FROM pg_database WHERE datname LIKE 'temporal%';"   # -> 2
# All server Deployments available
kubectl $CTX -n temporal get deploy      # frontend/history/matching/worker/internal-frontend/web/admintools all 1/1
# Cluster serving
temporal operator cluster health --address 127.0.0.1:7233                                    # -> SERVING
# JWKS reachable by the frontend (RBAC mode)
kubectl $CTX -n temporal exec deploy/temporal-frontend -- wget -qO- http://temporal-jwks/jwks.json | head -c 40
```

---

## Running everything

```bash
# Unit (no cluster needed):
cd workers && go test ./... && cd ..

# Integration + RBAC + smoke (needs the local cluster from runbooks/runbook-local-rancher-desktop.md):
#   follow runbooks/runbook-local-rancher-desktop.md §2–6, then the assertions in sections 3–5 above.
```

## Continuous integration (recommended)

- **On every pull request:** `go test ./...` (unit) and, once implemented, the
  replay test (§2). Both run without a cluster, so they're fast and required.
- **Nightly / pre-release:** spin up a disposable Kubernetes cluster (kind or a
  Rancher runner), run the e2e and RBAC suites (§3–4), tear down.

## Coverage status

Automated today: unit tests for both teams' workflows. Manual but executed and
passing: the e2e, RBAC, and smoke checks (via the runbook). Planned: the replay
determinism gate and scripting the e2e/RBAC suites for CI. Anything not yet
automated is called out above rather than implied to be covered.
