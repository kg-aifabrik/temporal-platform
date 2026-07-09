# Writing workflows on the shared Temporal platform

This document is an overview for a team building, testing, and deploying their
own workflows on the shared Temporal infrastructure. For the rest of the
document we use **team-c** as a team that is coming on board. You can use
**team-a** — the bare-metal → OS → Kubernetes provisioning pipeline in
[`workers/team-a`](../workers/team-a) — as a reference during your journey.

You don't operate the cluster; the platform team does. You write code that
connects to it. The platform is assumed to be already running (see
[`runbook.md`](runbook.md)).

## Mental model

There are primarily four concepts. Once they click, the rest of this guide is
just detail.

- **Workflow** — your orchestration code: what happens, and in what order. It is
  durable, so if the process running it crashes, Temporal replays the workflow's
  history and picks up where it left off. That replay is why workflow code must
  be deterministic: no reading the clock, no random numbers, no network calls
  directly inside it. *Example: team-a's workflow says "allocate the hardware,
  install the OS, build Kubernetes, then verify." It's the recipe, not the
  cooking.*
- **Activity** — a plain function that does the real work: call an API, write a
  file, wait on something slow. Activities are allowed to fail, and Temporal
  retries them for you. Anything non-deterministic lives here. *Example: "install
  the OS by calling Rafay" is an activity; if Rafay times out, Temporal runs it
  again.*
- **Worker** — your process. It holds your workflow and activity code and polls a
  task queue for work to run. Your team owns and runs it; the platform team runs
  the cluster, not your worker. *Example: team-c's worker runs team-c's code and
  watches the `team-c-tq` queue.*
- **Task queue** — a named line that connects a workflow start to your worker.
  Starting a workflow drops work on the queue; your worker picks it up. *Example:
  work for team-c waits on `team-c-tq` until team-c's worker pulls it.*

Putting it together: you start a workflow → the cluster puts the first task on
your queue → your worker picks it up and runs the workflow → the workflow asks
for activities, which go back on the queue → your worker runs those too → the
workflow finishes, and its result and full history are saved.

## Onboarding

Getting a team onto the cluster is a one-time setup that splits across two roles:

- **The platform operator** creates the team's namespace and issues its tokens,
  once, when the team comes on board.
- **The team** builds, runs, and debugs workflows against that namespace from
  then on.

Below, the operator onboards **team-c** (members: erin, frank, grace).

### What the operator needs first

- **Access to the cluster.** The operator reaches Temporal's frontend through
  Kubernetes, so they need a working `KUBECONFIG` pointed at the cluster
  (locally, the `rancher-desktop` context) and a port-forward running:
  ```bash
  kubectl -n temporal port-forward svc/temporal-frontend 7233:7233
  ```
- **The `temporal` CLI** — `brew install temporal`.
- **An admin token.** With access control on, creating a namespace is an
  admin-only action, so the operator passes a token that carries
  `temporal-system:admin`. Locally that is `auth/out/tokens/admin.jwt`.

### Step 1 — create the namespace

A namespace is the team's own space: its workflows, task queues, and access all
live inside it.

```bash
temporal operator namespace create --address 127.0.0.1:7233 --retention 72h \
  --description "Team C workflows" team-c \
  --grpc-meta "authorization=Bearer $(cat auth/out/tokens/admin.jwt)"
```

In production the operator also sets per-namespace rate limits here, so one team
can't crowd out the others (see the research repo's `multi-tenancy-setup.md`).

### Step 2 — issue tokens

Every caller — each member, and the team's worker — proves who it is with a JSON
Web Token (JWT). The frontend checks that token on each request and allows or
denies it based on a `permissions` claim inside: `team-c:write` lets you change
team-c's workflows, `temporal-system:read` lets you read every team's.

Locally these tokens are minted by [`auth/tokengen`](../auth/tokengen). Add
team-c to its list — three members who can read everything and write their own
team, plus one worker token:

```go
// auth/tokengen/main.go — add to `identities`
{"erin", "erin@corp.local", []string{"temporal-system:read", "team-c:write"}},
{"frank", "frank@corp.local", []string{"temporal-system:read", "team-c:write"}},
{"grace", "grace@corp.local", []string{"temporal-system:read", "team-c:write"}},
{"worker-team-c", "worker-team-c", []string{"team-c:write"}},
```

Then regenerate:

```bash
go run ./auth/tokengen -out ./auth/out   # writes tokens/{erin,frank,grace,worker-team-c}.jwt
```

Nothing else on the cluster changes. tokengen signs with the key the frontend
already trusts, so the new tokens work right away — no restart. (How the signing
key and the key set the frontend validates against fit together is in
[`runbook.md`](runbook.md) §6.)

Production does this step differently: an identity provider (Google, Okta,
Keycloak) issues tokens when people log in, and you add the three members to a
`team-c` group that maps to the `team-c:write` claim. Temporal itself doesn't
change.

### How long tokens last, and how to refresh them

- **Locally**, tokengen tokens last a year, so you rarely think about them. When
  one expires, or you just want to rotate it, run `go run ./auth/tokengen`
  again. It reuses the same signing key, so the fresh token works immediately and
  nothing on the cluster needs to change.
- **In production**, identity-provider tokens are short-lived (minutes to hours)
  and refreshed automatically — a login session or the worker's credentials
  provider fetches a new one behind the scenes, so no one regenerates tokens by
  hand.

### The handoff

The operator gives team-c four things. Everything the team runs is configured
from them:

| What | team-c's value |
|---|---|
| Frontend address | `127.0.0.1:7233` (through the port-forward above) |
| Namespace | `team-c` |
| Task queue (named `<team>-tq`) | `team-c-tq` |
| Tokens | `worker-team-c.jwt` for the worker, one per member (`erin.jwt`, …) |

The team points its worker at these with environment variables. The shared
client in [`workers/internal/temporalclient`](../workers/internal/temporalclient)
reads them:

```bash
export TEMPORAL_ADDRESS=127.0.0.1:7233
export TEMPORAL_NAMESPACE=team-c
export TEMPORAL_AUTH_TOKEN=$(cat auth/out/tokens/worker-team-c.jwt)   # only when RBAC is on
```

## Your first workflow

Start with one activity and a tiny workflow, and get it running before adding
anything else.

### Step 1 — a package for your team

team-a and team-b live as `package main` under the one `workers` Go module. Add
yours the same way — no new module:

```bash
mkdir workers/team-c    # main.go (worker + workflow) and workflow_test.go go here
```

If your team keeps its workers in its own repo instead, run `go mod init` there
and `go get go.temporal.io/sdk@latest`. The worker connects to the same cluster
either way.

### Step 2 — an activity

An activity is just a function. Its first argument is `context.Context`, and it
returns a result and an error.

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

Two things to notice: it takes `workflow.Context`, not `context.Context`, and it
calls the activity through `workflow.ExecuteActivity` rather than calling
`Greet(...)` directly. That indirection is what lets Temporal replay it.

### Step 4 — the worker

The worker connects, registers your code, and polls the task queue.

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

With the environment exported from the handoff (`TEMPORAL_NAMESPACE=team-c`, and
under RBAC `TEMPORAL_AUTH_TOKEN=$(cat auth/out/tokens/worker-team-c.jwt)`):

```bash
cd workers && go run ./team-c   # logs: "Started Worker ... TaskQueue team-c-tq"

# in another terminal — add a member token under RBAC:
temporal workflow start -n team-c --task-queue team-c-tq \
  --type GreetingWorkflow --workflow-id greet-1 --input '"world"' \
  --grpc-meta "authorization=Bearer $(cat auth/out/tokens/erin.jwt)"

temporal workflow show -n team-c --workflow-id greet-1   # -> result: "hello world"
```

That's the whole loop. Everything else is making the workflow do more.

## Growing into a real pipeline (team-a)

[`workers/team-a/main.go`](../workers/team-a/main.go) is the same shape, grown up.
Three things it adds that yours will too:

**Several activities in sequence**, each result feeding the next:

```go
if err := workflow.ExecuteActivity(ctx, AllocateBareMetal, req).Get(ctx, &allocated); err != nil {
    return "", err
}
// ... InstallOS per node, ConfigureNetwork, InstallKubernetes, VerifyCluster
```

**Retries and timeouts** as policy, so you never write a retry loop:

```go
ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
    StartToCloseTimeout: time.Minute,
    RetryPolicy: &temporal.RetryPolicy{
        InitialInterval: time.Second,
        MaximumAttempts: 5,
    },
})
```

team-a's `InstallOS` fails on its first attempt on purpose and succeeds on the
second. Temporal retries it for you, and you see attempt 2 in the UI.

**Durable timers** instead of `time.Sleep`. Inside a workflow, use
`workflow.Sleep`. It survives a crash and is skipped instantly in tests:

```go
_ = workflow.Sleep(ctx, 2*time.Second)
```

As you grow, you'll also reach for child workflows (split a big workflow into
smaller ones), signals (send data into a running workflow), queries (read its
state), continue-as-new (restart a long-runner with a fresh history), and
Schedules (a cron replacement).

## Test before you deploy

This matters a lot in Temporal: because a workflow replays its history, a code
change can break workflows that are already running. Two tests catch that.

**A unit test with time-skipping.** Mock the activities, run the workflow, and
check the result. No server needed, and it finishes in milliseconds. See
[`workers/team-a/workflow_test.go`](../workers/team-a/workflow_test.go):

```bash
cd workers && go test ./team-a/ -v
# --- PASS: TestProvisionClusterWorkflow (0.04s)
```

team-c writes its own `workers/team-c/workflow_test.go` in the same shape — copy
team-a's or team-b's as a template. The whole suite runs with `go test ./...`.

**A replay test, the safety net.** Before you deploy a change, replay real
histories against the new code. If the logic diverges, the replayer fails, and
you've caught the problem before it reaches a running workflow. Wire this into
continuous integration so a bad change can't merge.

## Commit your work

What lands in the repo, and what must never:

- **Commit** your team's code under `workers/team-c/` (worker, workflow,
  activities, and `workflow_test.go`). Locally, also commit the team-c identities
  you added to [`auth/tokengen/main.go`](../auth/tokengen/main.go) — that's the
  record of who has access in the demo. In production that lives in the identity
  provider, not the repo.
- **Never commit** minted tokens or the signing key. `auth/out/` and
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

## Debugging a workflow

Work from the outside in. The UI shows what happened, the CLI gets you the
details, and the shape of the failure tells you where to look. The examples use
team-a because it has runs to inspect; for team-c, swap in `-n team-c` and a
team-c member token (for example `erin.jwt`).

### Start in the Web UI

Run `kubectl -n temporal port-forward svc/temporal-web 8080:8080`, open
<http://localhost:8080>, pick your namespace, and click the workflow. What to
read:

- **Event history** — the full timeline: every activity scheduled, started,
  completed, failed, and retried. This is the source of truth.
- **Stack trace** tab — for a running workflow, the exact line your code is
  blocked on (waiting on an activity or a timer).
- **Pending Activities** — an activity that's retrying shows here with its
  attempt count and the last failure message. This is usually the answer.

### Then the CLI

```bash
temporal workflow describe -n team-a --workflow-id prov-edge-01   # status, pending activities, task queue
temporal workflow show     -n team-a --workflow-id prov-edge-01   # full event history
temporal workflow list     -n team-a --query 'ExecutionStatus="Failed"'   # find the broken ones
```

Add `--grpc-meta "authorization=Bearer $(cat auth/out/tokens/alice.jwt)"` when
RBAC is on.

### Match the symptom to the cause

| Symptom | Most likely cause | What to check |
|---|---|---|
| Workflow stuck in `Running`, no events after start | No worker is polling your task queue | Is the worker up? Right `--task-queue`? Right `TEMPORAL_NAMESPACE`? Rising **schedule-to-start latency** in `describe` confirms it. |
| Same, and RBAC is on | Worker's token lacks `write` on the namespace | The worker needs a `team-x:write` token; a read-only token can't poll. |
| Activity keeps retrying | The activity errors | Pending Activities → last failure and attempt count. Fix the activity; Temporal retries it, or reset (below). |
| `Request unauthorized` | Missing, expired, or wrong-namespace token | Check the token's `permissions` claim and that it's passed as `authorization: Bearer …`. |
| `nondeterministic error` after a deploy | Workflow code changed incompatibly | You changed the order or shape of activity calls under a running workflow. Confirm with the replayer, and version the change (patching API / Worker Versioning). |
| Activity times out | Timeout too tight, or the work is genuinely slow | `StartToCloseTimeout` bounds one attempt; `ScheduleToStartTimeout` means it waited too long for a free worker (scale workers). |

### Re-running after a fix: reset

Once you've fixed a bug, you don't start over. Reset rewinds a workflow to an
earlier point in its history and replays forward with the new code:

```bash
temporal workflow reset -n team-a --workflow-id prov-edge-01 \
  --type LastWorkflowTask --reason "fixed InstallOS bug"
```

### Fastest local loop

For pure logic changes you don't even need the cluster. Run everything in one
process with `temporal server start-dev` (an in-memory server plus UI on
`localhost:8233`), point your worker at it, and iterate. Move to the shared
cluster once it works.

## Deploying your worker for real

Locally you run the worker as a host process. In production it's a Kubernetes
Deployment built from [`workers/Dockerfile`](../workers/Dockerfile), one image
per team. Roll out new versions with Worker Versioning so in-flight workflows
finish on the code that started them. See [`deploy/gcp/`](../deploy/gcp).

## Checklist

- [ ] **Onboarded**: namespace created, and (under RBAC) member and worker tokens issued.
- [ ] Namespace, task queue (`<team>-tq`), and token wired into the worker's environment.
- [ ] Workflow is deterministic — all I/O, clocks, and randomness in activities.
- [ ] Activities have timeouts and a retry policy.
- [ ] `workflow.Sleep`, not `time.Sleep`, inside workflows.
- [ ] A time-skipping unit test, and a replay test in CI.
- [ ] Worker registers every workflow and activity it uses.
