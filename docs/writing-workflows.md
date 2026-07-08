# Writing your first workflow (a team guide)

How a team goes from nothing to a running, debuggable workflow on the shared
Temporal cluster. It follows a brand-new team, **team-c** (three members), from
onboarding through build, test, commit, and debug — and points at **team-a**
(the bare-metal → OS → Kubernetes provisioning pipeline in
[`workers/team-a`](../workers/team-a)) as the mature reference to grow toward.

Assumes the platform is already up (see [`runbook.md`](runbook.md)). You do
**not** operate the cluster; you write code that connects to it.

## The mental model (read this first)

Four concepts, and the whole rest of the guide is just filling them in:

- **Workflow** — your orchestration code. It says *what happens in what order*.
  It is durable: if a process crashes mid-run, Temporal replays its history and
  continues exactly where it left off. Because of replay, workflow code must be
  **deterministic** — no direct clocks, randomness, network calls, or goroutines.
  Anything non-deterministic goes in an activity.
- **Activity** — a plain function that does the real, messy work (call an API,
  write a file, sleep). Activities can fail; Temporal retries them per your
  policy. This is where I/O lives.
- **Worker** — your process. It hosts your workflow + activity code and polls a
  **task queue** for work. You run and own it; the platform team runs the
  cluster.
- **Task queue** — a named channel. A workflow start puts work on a queue; your
  worker pulls from it. It is the routing between "start" and "your code".

The flow: you *start* a workflow (CLI, code, or a schedule) → the cluster puts a
task on your team's queue → your *worker* picks it up, runs the workflow, which
*schedules activities* back onto the queue → your worker runs those too → the
workflow completes and its result + full history are stored.

## Onboarding a new team — worked example: team-c (3 members)

Bringing a team onto the shared cluster is two roles' work: a **platform
operator** creates the team's space and credentials once, then the **team**
builds against it. Here we onboard **team-c** with three members — erin, frank,
and grace.

### Operator: create the namespace

A namespace is the team's isolated space — its own workflows, task queues, and
(under RBAC) access. One command:

```bash
temporal operator namespace create --address 127.0.0.1:7233 --retention 72h \
  --description "Team C workflows" team-c
```

(Production also sets per-namespace rate limits here, so one team can't starve
the others — see the research repo's `multi-tenancy-setup.md`. Not needed locally.)

### Operator: issue credentials

Under RBAC every caller needs a token. Locally these are minted by
[`auth/tokengen`](../auth/tokengen); add team-c's identities to its list — three
members who can read every team and write their own, plus one worker token:

```go
// auth/tokengen/main.go — add to `identities`
{"erin", "erin@corp.local", []string{"temporal-system:read", "team-c:write"}},
{"frank", "frank@corp.local", []string{"temporal-system:read", "team-c:write"}},
{"grace", "grace@corp.local", []string{"temporal-system:read", "team-c:write"}},
{"worker-team-c", "worker-team-c", []string{"team-c:write"}},
```

Regenerate:

```bash
go run ./auth/tokengen -out ./auth/out    # adds tokens/{erin,frank,grace,worker-team-c}.jwt
```

Nothing else on the cluster changes: tokengen reuses the existing signing key, so
the new tokens are signed by a key the frontend already trusts — no JWKS update,
no Temporal restart. (In production the identity provider does this: add the
three people to a `team-c` group that maps to the `team-c:write` claim — again,
no change to Temporal.)

### The handoff

The operator hands team-c four things; everything the team runs is configured
from them:

| What | Value for team-c |
|---|---|
| Frontend address | `127.0.0.1:7233` (via `kubectl -n temporal port-forward svc/temporal-frontend 7233:7233`) |
| Namespace | `team-c` |
| Task queue (convention `<team>-tq`) | `team-c-tq` |
| Tokens | `worker-team-c.jwt` for the worker + one per member (`erin.jwt`, …) |

The shared client in [`workers/internal/temporalclient`](../workers/internal/temporalclient)
reads these from the environment:

```bash
export TEMPORAL_ADDRESS=127.0.0.1:7233
export TEMPORAL_NAMESPACE=team-c
export TEMPORAL_AUTH_TOKEN=$(cat auth/out/tokens/worker-team-c.jwt)   # only if RBAC is on
```

---

## 0 → 1: team-c's first workflow

Start with one activity and a one-line workflow. Get *that* running before
adding anything.

### Step 1 — a package for your team

team-a and team-b live as `package main` under the one `workers` Go module. Add
yours the same way — no new module:

```bash
mkdir workers/team-c    # main.go (worker + workflow) and workflow_test.go go here
```

(If your team keeps its workers in its **own repo** instead, `go mod init` there
and `go get go.temporal.io/sdk@latest` — the worker connects to the same cluster
either way.)

### Step 2 — an activity

An activity is just a function. Its first argument is `context.Context`; it
returns `(result, error)`.

```go
func Greet(ctx context.Context, name string) (string, error) {
    return "hello " + name, nil
}
```

### Step 3 — a workflow that calls it

```go
func GreetingWorkflow(ctx workflow.Context, name string) (string, error) {
    ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
        StartToCloseTimeout: time.Minute, // how long the activity may take
    })
    var greeting string
    err := workflow.ExecuteActivity(ctx, Greet, name).Get(ctx, &greeting)
    return greeting, err
}
```

Note it takes `workflow.Context` (not `context.Context`) and calls the activity
through `workflow.ExecuteActivity` — never by calling `Greet(...)` directly. That
indirection is what makes it replayable.

### Step 4 — the worker

The worker connects, registers your code, and polls a task queue.

```go
func main() {
    c, _ := temporalclient.New()   // reads TEMPORAL_ADDRESS/NAMESPACE/AUTH_TOKEN
    defer c.Close()

    w := worker.New(c, "team-c-tq", worker.Options{})
    w.RegisterWorkflow(GreetingWorkflow)
    w.RegisterActivity(Greet)
    _ = w.Run(worker.InterruptCh())
}
```

### Step 5 — run it and start one

With the environment exported from the handoff (`TEMPORAL_NAMESPACE=team-c` and,
under RBAC, `TEMPORAL_AUTH_TOKEN=$(cat auth/out/tokens/worker-team-c.jwt)`):

```bash
cd workers && go run ./team-c   # worker logs: "Started Worker ... TaskQueue team-c-tq"

# in another terminal — add a member token under RBAC:
temporal workflow start -n team-c --task-queue team-c-tq \
  --type GreetingWorkflow --workflow-id greet-1 --input '"world"' \
  --grpc-meta "authorization=Bearer $(cat auth/out/tokens/erin.jwt)"

temporal workflow show -n team-c --workflow-id greet-1   # -> result: "hello world"
```

That's 0 → 1. Everything else is making the workflow do more.

---

## 1 → N: growing into a real pipeline (team-a)

[`workers/team-a/main.go`](../workers/team-a/main.go) is the same shape, grown up.
Three things it adds that yours will too:

**Multiple activities in sequence**, each result feeding the next:

```go
if err := workflow.ExecuteActivity(ctx, AllocateBareMetal, req).Get(ctx, &allocated); err != nil {
    return "", err
}
// ... InstallOS per node, ConfigureNetwork, InstallKubernetes, VerifyCluster
```

**Retries and timeouts**, declared once as policy — you don't write retry loops:

```go
ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
    StartToCloseTimeout: time.Minute,
    RetryPolicy: &temporal.RetryPolicy{
        InitialInterval: time.Second,
        MaximumAttempts: 5,
    },
})
```

`InstallOS` deliberately fails on its first attempt and succeeds on the second —
Temporal retries it automatically. You can see this in the UI as attempt 2.

**Durable timers** instead of `time.Sleep`. In workflow code use
`workflow.Sleep` — it survives crashes and is skipped instantly in tests:

```go
_ = workflow.Sleep(ctx, 2*time.Second)
```

Other tools you'll reach for as you grow: **child workflows** (break a big
workflow into smaller ones), **signals** (send data into a running workflow),
**queries** (read its state), **continue-as-new** (restart a long-running
workflow with a fresh history), and **Schedules** (cron replacement).

---

## Test before you deploy

This matters more in Temporal than most systems: because workflows replay,
a code change can break workflows that are *already running*. Two tests catch
that.

**Unit test with time-skipping.** Mock the activities, run the workflow, assert
the outcome — no server needed, milliseconds to run. See
[`workers/team-a/workflow_test.go`](../workers/team-a/workflow_test.go):

```bash
cd workers && go test ./team-a/ -v
# --- PASS: TestProvisionClusterWorkflow (0.04s)
```

team-c writes its own `workers/team-c/workflow_test.go` in exactly this shape —
copy team-a's or team-b's test as the template. The whole suite runs with
`go test ./...` from `workers/`.

**Replay test (the safety net).** Before deploying a code change, replay real
production histories against the new code; if the logic diverges, the replayer
fails — catching non-determinism before it reaches a running workflow. Wire this
into continuous integration (CI) so a bad change never merges.

---

## Commit your work

What lands in the repo — and what must never:

- **Commit:** your team's code under `workers/team-c/` (worker, workflow,
  activities, and `workflow_test.go`). Locally, also commit the team-c
  identities you added to [`auth/tokengen/main.go`](../auth/tokengen/main.go) —
  that's the source of truth for who has access in the demo. In production that
  lives in the identity provider, not the repo.
- **Never commit:** minted tokens or the signing key. `auth/out/` and
  `workers/bin/` are already in [`.gitignore`](../.gitignore); tokens are
  throwaway and regenerated with `go run ./auth/tokengen`.

The gate before a pull request is green tests, then a branch and PR:

```bash
cd workers && go test ./...     # your team-c test passes alongside team-a/team-b
cd ..
git checkout -b team-c-onboarding
git add workers/team-c auth/tokengen/main.go
git commit -m "team-c: onboard with greeting workflow"
git push -u origin team-c-onboarding
```

---

## Debugging a workflow

When a run misbehaves, work from the outside in: the UI shows *what* happened,
the CLI gets you *details*, and the failure's shape tells you *where* to look.
The examples below use team-a because it has runs to inspect; for team-c, swap
in `-n team-c` and a team-c member token (e.g. `erin.jwt`).

### Start in the Web UI

`kubectl -n temporal port-forward svc/temporal-web 8080:8080`, open
<http://localhost:8080>, pick your namespace, click the workflow. What to read:

- **Event history** — the whole timeline: every activity scheduled, started,
  completed, failed, retried. This is the source of truth.
- **Stack trace** tab — for a *running* workflow, shows the exact line your code
  is blocked on (e.g. waiting on an activity or a timer).
- **Pending Activities** — an activity that's retrying shows here with its
  attempt count and the **last failure message**. This is usually the answer.

### Then the CLI

```bash
temporal workflow describe -n team-a --workflow-id prov-edge-01   # status, pending activities, task queue
temporal workflow show     -n team-a --workflow-id prov-edge-01   # full event history
temporal workflow list     -n team-a --query 'ExecutionStatus="Failed"'   # find the broken ones
```

(Add `--grpc-meta "authorization=Bearer $(cat auth/out/tokens/alice.jwt)"` when
RBAC is on.)

### Match the symptom to the cause

| Symptom | Most likely cause | What to check |
|---|---|---|
| Workflow stuck in `Running`, no events after start | No worker is polling your task queue | Is the worker process up? Right `--task-queue`? Right `TEMPORAL_NAMESPACE`? Rising **schedule-to-start latency** in `describe` confirms it. |
| Same, and RBAC is on | Worker's token lacks `write` on the namespace | Worker needs a `team-x:write` token — a read-only token can't poll. |
| Activity keeps retrying | The activity errors | Pending Activities → last failure + attempt count. Fix the activity; Temporal retries it automatically, or reset (below). |
| `Request unauthorized` | Missing/expired/wrong-namespace token | Check the token's `permissions` claim and that it's passed as `authorization: Bearer …`. |
| `nondeterministic error` after a deploy | Workflow code changed incompatibly | You changed the order/shape of activity calls under a running workflow. Use the replayer to confirm, and version the change (patching API / Worker Versioning). |
| Activity times out | Timeout too tight, or work genuinely too slow | `StartToCloseTimeout` bounds a single attempt; `ScheduleToStartTimeout` means it waited too long for a free worker (scale workers). |

### Re-running after a fix: reset

Once you've fixed a bug, you don't have to start over. **Reset** rewinds a
workflow to an earlier point in its history and replays forward with the new
code:

```bash
temporal workflow reset -n team-a --workflow-id prov-edge-01 \
  --type LastWorkflowTask --reason "fixed InstallOS bug"
```

### Fastest local loop

For pure logic iteration you don't even need the cluster: run everything in one
process with `temporal server start-dev` (an in-memory server + UI on
`localhost:8233`), point your worker at it, and iterate. Promote to the shared
cluster once it works.

---

## Deploying your worker for real

Locally you run the worker as a host process. In production it's a Kubernetes
Deployment built from [`workers/Dockerfile`](../workers/Dockerfile) (one image
per team). Roll out new versions safely with Worker Versioning so in-flight
workflows finish on the code that started them. See [`deploy/gcp/`](../deploy/gcp).

## Checklist

- [ ] **Onboarded**: namespace created, and (if RBAC) member + worker tokens issued.
- [ ] Namespace, task queue (`<team>-tq`), and token wired into the worker's environment.
- [ ] Workflow is deterministic — all I/O, clocks, randomness in activities.
- [ ] Activities have timeouts and a retry policy.
- [ ] `workflow.Sleep`, not `time.Sleep`, inside workflows.
- [ ] A time-skipping unit test, and a replay test in CI.
- [ ] Worker registers every workflow and activity it uses.
