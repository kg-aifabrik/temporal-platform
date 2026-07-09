# Concurrency, long-running activities, and idempotency

The next level after [`writing-workflows.md`](writing-workflows.md): how to throttle
how much team-c runs at once, how to write an activity that takes hours, and how
to make activities safe to retry. Examples continue with team-c and team-a.

## Throttling concurrency

First, a clarification, because it trips people up. A running workflow is cheap:
it mostly sits idle waiting on activities and timers, and it isn't "resident" in
a worker. So "throttle the concurrency of workflows" almost always means one of
two concrete things: **how many activities/workflow tasks execute at once**, and
**how fast work is dispatched**. Those are the knobs below.

The controls live at three levels.

### 1. Worker options (per team)

These are set on the worker your team ships, so in practice they are your team's
knobs. Defaults are large on purpose (Temporal assumes you want throughput):

| `worker.Options` field | Controls | Default |
|---|---|---|
| `MaxConcurrentActivityExecutionSize` | Activities running at once **in one worker pod** | 1000 |
| `MaxConcurrentWorkflowTaskExecutionSize` | Workflow tasks processed at once in one pod | 1000 |
| `MaxConcurrentLocalActivityExecutionSize` | Local activities at once in one pod | 1000 |
| `MaxConcurrentActivityTaskPollers` / `...WorkflowTaskPollers` | Poller goroutines per pod | 2 |
| `WorkerActivitiesPerSecond` | Activity start rate **per pod** | 100000 (∞) |
| `TaskQueueActivitiesPerSecond` | Activity start rate across the **whole task queue** (all pods) | 100000 (∞) |

```go
w := worker.New(c, "team-c-tq", worker.Options{
    MaxConcurrentActivityExecutionSize: 50,   // at most 50 activities per pod
    TaskQueueActivitiesPerSecond:       20,    // at most 20 activity starts/sec across all team-c pods
})
```

The distinction that matters: `MaxConcurrentActivityExecutionSize` is **per pod**,
so team-c's real limit is that times the replica count (which the platform
scales). To cap team-c *as a whole* regardless of how many pods run, use
`TaskQueueActivitiesPerSecond` — it's enforced across the entire task queue.

### 2. Per-workflow throttling

Temporal has **no built-in "run at most N workflows of type X"** switch. When you
need it, pick one:

- **Bound fan-out inside a workflow** with a semaphore. This caps concurrent
  activities (or child workflows) started by a single workflow execution — e.g.
  install the OS on at most 3 nodes at a time:

  ```go
  sem := workflow.NewSemaphore(ctx, 3)
  for _, node := range nodes {
      node := node
      _ = sem.Acquire(ctx, 1)
      workflow.Go(ctx, func(ctx workflow.Context) {
          defer sem.Release(1)
          _ = workflow.ExecuteActivity(ctx, InstallOS, node).Get(ctx, nil)
      })
  }
  ```

  **Scope: one workflow execution.** This limit is not per-pod or cluster-wide.
  If ten provisioning workflows run at once, each gets its own limit of 3 — so up
  to 30 `InstallOS` activities run across the fleet. To turn it into a real global
  cap, either run the whole batch as a *single* workflow (one execution then
  equals the whole job — see *Worked example* below) or enforce the limit with a
  shared task-queue ceiling.

- **Give the workflow type its own task queue** and a worker sized for it, then
  cap that queue with `TaskQueueActivitiesPerSecond`. A task queue is just a
  name, so this is cheap.
- **Throttle at the start** — rate-limit how fast you call `StartWorkflow`, or
  route starts through a single "gatekeeper" workflow that only lets N children
  run at once.

So: throttling is per **team** (worker options, task-queue rate) or per **task
queue**; a per-workflow-type cap is something you build with one of the patterns
above.

### 3. The platform's ceiling (per namespace)

Above your knobs, the platform team caps each namespace server-side (requests per
second and database queries per second per namespace) so no single team can
overwhelm the shared cluster. That cap is per team (per namespace) and is set by
the platform, not you — your worker settings tune throughput *up to* that ceiling.
See the research repo's `multi-tenancy-setup.md`.

## Worked example: 1000 machines, 100 installs at a time

Goal: provision 1000 machines, but never more than 100 installs running at once,
so the image servers and the cluster aren't overwhelmed. Use two layers — one for
the *intended* limit, one as a *guardrail* that holds no matter what the workflow
code does.

**Layer 1 — the intended limit: one batch workflow with a semaphore.** Because a
semaphore is scoped to a single execution, run the whole batch as *one* workflow.
Its per-execution limit of 100 is then the global limit for the job:

```go
func ProvisionFleetWorkflow(ctx workflow.Context, machines []string) error {
    sem := workflow.NewSemaphore(ctx, 100)   // at most 100 in flight for this job
    wg := workflow.NewWaitGroup(ctx)
    for _, m := range machines {
        m := m
        _ = sem.Acquire(ctx, 1)              // blocks the loop once 100 are running
        wg.Add(1)
        workflow.Go(ctx, func(ctx workflow.Context) {
            defer wg.Done()
            defer sem.Release(1)
            // one child workflow per machine (allocate -> OS -> k8s)
            _ = workflow.ExecuteChildWorkflow(ctx, ProvisionMachineWorkflow, m).Get(ctx, nil)
        })
    }
    wg.Wait(ctx)
    return nil
}
```

`Acquire` blocks the loop once 100 are in flight, so machine 101 waits for one to
finish. One execution means the 100 cap is global for this batch.

**Layer 2 — the guardrail: a dedicated task queue with a hard ceiling.** The
semaphore is workflow *logic*; a bug, or a second batch started by someone else,
could still push past 100. Protect the shared resources regardless by running the
install activity on its own task queue with a worker fleet whose total capacity is
the ceiling:

- `MaxConcurrentActivityExecutionSize` so the install worker pods can't run more
  than the cap between them (e.g. one pod at 100, or a fixed per-pod × replica
  budget). The worker simply stops pulling work at the limit.
- `TaskQueueActivitiesPerSecond` as well if the image servers care about request
  *rate*, not just concurrency.

With the install activities isolated on their own queue and worker, even two
batches running at once can't drive the image servers past the ceiling.

**If several independent jobs must share one global budget** — not one batch, but
many separate workflows across pods sharing a pool of 100 — the in-workflow
semaphore isn't enough, because each job gets its own 100. Two options: rely on
the Layer-2 task-queue ceiling (the worker fleet *is* the shared limit, which is
usually the simplest answer), or run a **gatekeeper workflow** — one long-lived
workflow holding 100 leases that the others signal to acquire and release before
and after installing. The API-driven case below is exactly this situation.

**Keep history bounded.** One workflow driving 1000 children writes a few thousand
history events, which is fine. Far beyond that (tens of thousands of events),
process the machines in chunks and `workflow.NewContinueAsNewError` between chunks
so the workflow's history stays small.

## When work arrives through an API (one host per call)

Often there's no batch at all: an API is called once per host — 1000 times for
1000 machines — and you don't know the total or the arrival rate in advance. There
is no parent workflow to hold a semaphore, so the cap has to live outside any
single execution. Three things make this clean.

**1. The API just starts a workflow, and dedupes on host ID.** Each call starts
one per-host workflow and returns immediately (e.g. HTTP 202 with the workflow ID
as the handle). Use the host ID as the workflow ID so a retried or duplicate call
doesn't provision twice:

```go
_, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
    ID:                       "provision-" + hostID,   // one workflow per host
    TaskQueue:                "provisioning-tq",
    WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING, // idempotent
}, ProvisionMachineWorkflow, hostID)
```

`USE_EXISTING` makes a second call for a host already being provisioned attach to
the running workflow instead of erroring, so the API is naturally retry-safe.
Starting a workflow is cheap and durable — once accepted it will run even if
everything downstream is saturated, so the API never has to reject or block.

**2. Throttle the scarce step, not the API.** Don't try to cap how many workflows
start; cap the step that actually stresses the image servers. Run `InstallOS` on a
dedicated install task queue whose worker fleet has a total capacity of 100
(`MaxConcurrentActivityExecutionSize` × replicas). However many workflows are open,
only 100 installs run at once — that's the fleet's capacity — and the rest sit in
the task-queue backlog, starting as slots free up. That backlog **is** your
backpressure: nothing is dropped, and the cap holds no matter how fast requests
arrive. Add `TaskQueueActivitiesPerSecond` too if the image servers care about
request rate.

**3. Don't set a tight `ScheduleToStartTimeout` on the throttled activity.** Waiting
in the backlog is the expected, healthy state here, so that timeout would just fail
installs that are correctly queued (the SDK advises against setting it at all
outside host-specific routing). Instead, watch the queue's **backlog** and
**schedule-to-start latency**; a sustained rise is your signal to add install
workers (raise the cap).

**When you need the cap independent of fleet size, or want fairness/priority**, use
the gatekeeper (resource-pool) workflow from the previous section: one long-lived
workflow holding 100 permits that each host workflow signals to acquire before
installing and release after. It's an explicit global semaphore across executions,
at the cost of a hot singleton you must continue-as-new periodically, plus lease
timeouts so a crashed holder's permit is reclaimed. Reach for it only if the
task-queue ceiling isn't enough.

Bottom line: **accept every request as a durable workflow, and let a
capacity-limited task queue meter the installs.** Unknown volume becomes a queue
depth you monitor, not a number you must know up front.

## Long-running activities (a 2-hour OS install)

An activity that runs for hours is normal in Temporal. Three things make it work.

**1. Set the timeout to cover the whole run.** `StartToCloseTimeout` bounds a
single attempt, so it must be at least as long as the work (plus margin):

```go
ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
    StartToCloseTimeout: 3 * time.Hour,   // the install may take ~2h
    HeartbeatTimeout:    2 * time.Minute, // but we expect a heartbeat every ≤2m
    RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
})
```

**2. Heartbeat while it runs.** Without a heartbeat, if the worker pod running the
install dies, Temporal can't tell for up to the full 3-hour `StartToCloseTimeout`.
With a short `HeartbeatTimeout`, a missed heartbeat is detected in ~2 minutes and
the activity is retried. Heartbeating is also how a cancellation reaches the
activity.

```go
func InstallOS(ctx context.Context, node string) (string, error) {
    for step := 0; step < totalSteps; step++ {
        if err := ctx.Err(); err != nil { // cancelled? stop.
            return "", err
        }
        doInstallStep(node, step)
        activity.RecordHeartbeat(ctx, step) // "I'm alive, and I'm on step N"
    }
    return "installed", nil
}
```

**3. If an external system does the work, complete asynchronously.** When the
install is driven by something that calls back later (a provisioning system,
a human approval), return `activity.ErrResultPending` and complete the activity
out of band via the client with the activity's task token, instead of blocking a
worker for two hours. Use this when the wait is truly external; otherwise the
heartbeat loop above is simpler.

## Idempotency and resuming after a retry

Because Temporal retries activities, **the same activity can run more than once**.
Its side effects must be safe to repeat — that's idempotency, and it's on you, not
the framework.

### The knobs

- **Retry policy** on `ActivityOptions.RetryPolicy`. Defaults if you set nothing:
  `InitialInterval` 1s, `BackoffCoefficient` 2.0, `MaximumInterval` 100×initial,
  `MaximumAttempts` 0 (unlimited). Set `MaximumAttempts` to bound retries, and
  list `NonRetryableErrorTypes` for failures that should never retry (a bad
  request won't get better by trying again).
- **`activity.GetInfo(ctx).Attempt`** tells you which attempt you're on (1-based).
- **A stable idempotency key.** Derive one from values that don't change across
  retries — the workflow ID is stable — and pass it to downstream systems so they
  dedupe. `key := workflow-id + "/" + node` reused on every attempt means the
  external "create host" call is a no-op the second time.
- **Non-retryable for permanent failures.** Return
  `temporal.NewNonRetryableApplicationError(...)` so Temporal stops instead of
  burning attempts.

### Persisting progress across retries

An activity can record progress as it goes and read it back on the next attempt,
so a retry resumes instead of starting over. The mechanism is **heartbeat
details**: whatever you pass to `RecordHeartbeat` is handed back to the next
attempt.

```go
func InstallOSAllNodes(ctx context.Context, nodes []string) error {
    start := 0
    if activity.HasHeartbeatDetails(ctx) {         // a previous attempt got partway
        var done int
        _ = activity.GetHeartbeatDetails(ctx, &done)
        start = done                                // resume from where it failed
    }
    for i := start; i < len(nodes); i++ {
        installOne(nodes[i])                        // must itself be idempotent
        activity.RecordHeartbeat(ctx, i+1)          // checkpoint: i+1 nodes done
    }
    return nil
}
```

If attempt 1 dies after node 3, attempt 2 reads `3` and starts at node 4.

### Two levels of checkpointing — pick the granularity

- **Across activities (workflow history).** Split a long job into several
  activities. Each completed activity is written to the workflow's history, so if
  the *workflow* is retried or replayed it never re-runs a finished activity. This
  is the default, cheapest checkpoint — reach for it first.
- **Within one activity (heartbeat details).** Use the pattern above when a single
  activity is long and you want to resume mid-flight without splitting it. More
  control, but you own the resume logic.

Rule of thumb: many small activities give you automatic checkpoints for free;
one big activity with heartbeats is for when the work genuinely can't be split.
