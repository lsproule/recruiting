# Runner capacity and scaling

The runner is the one process that grows with load: every submission is a
container, and a burst of candidates finishing at once is a burst of
containers. This note is how the runner queues that load, what it exposes to
an orchestrator, and how to run zero runners when there is nothing to run.

## Inside one runner

- `RUNNER_MAX_CONCURRENT` (default 2) executions run at once.
- `RUNNER_MAX_QUEUE` (default 4 × concurrency) further requests wait for a
  slot, each for at most `RUNNER_QUEUE_WAIT` (default 20s, clamped to 4m so a
  waiting request always answers inside the worker client's 5m timeout).
- A request past both bounds, or whose wait ran out, gets `503` with a
  `Retry-After` of `queue depth × average execution time ÷ capacity`, clamped
  to 2–60s. The average is a running estimate the runner keeps itself.
- A retry of an id that is still queued or running joins it rather than
  taking a second place; a finished id is answered from the result cache,
  bounded to `RUNNER_CACHE_MB` (default 64) and an hour, without a slot.

The worker's client waits out short 503s itself, up to two minutes in total.
Past that, or when the runner asks for longer than what is left, the
`runner.execute` job is snoozed for the `Retry-After` — River's snooze, which
consumes no attempt and applies no backoff — so a busy runner never burns a
submission's retries. Those jobs run on their own River queue (`runner`) with
`WORKER_RUNNER_CONCURRENCY` workers (default 4), so the worker's generic pool
cannot pile onto the runner: size it to about the runner's capacity plus its
queue, summed over runner replicas.

## What to scale on

The runner serves `/metrics` on `METRICS_ADDR` (default `:9090`, no secret)
and `GET /status` on its own listener (bearer `RUNNER_SECRET`), which answers
`{"in_flight", "queued", "capacity", "queue_capacity", "idle_seconds"}`.

| Series | Use |
| ------ | --- |
| `runner_queue_depth` | Scale **up** on it. A sustained depth means every slot is taken and requests are waiting; each waiting request is a candidate watching a spinner. |
| `runner_executions_in_flight` | Scale **down** when it, and the depth, sit at zero. |
| `runner_executions_total{status="rejected"}` | A rising rate means the queue itself overflowed: the worker is snoozing jobs. Capacity is short by more than one replica. |
| `runner_queue_wait_seconds` | The latency a candidate sees before their run starts; alert on the p90. |
| `runner_execution_seconds` | How long runs take; it sets what `Retry-After` the runner asks for. |
| `runner_last_execution_timestamp_seconds` | `time() - this` is how long a replica has been idle, for schedulers that reason in timestamps. |

A serviceable rule for a horizontal autoscaler (Kubernetes HPA on a custom
metric, Nomad's scaling stanza, or a cron reading Prometheus):

```
desired = ceil( (sum(runner_executions_in_flight) + sum(runner_queue_depth)) / RUNNER_MAX_CONCURRENT )
```

with a floor of 0 or 1 depending on how cold a start you can afford (a
runner container image is warm once pulled; the first execution of each
language pays a toolchain start), a stabilisation window of a few minutes on
the way down, and `min(desired, replicas that fit the host memory)` on the
way up: each execution can use the candidate's memory limit plus 128 MB of
headroom.

Because retries are idempotent by request id and a snoozed job simply comes
back later, scaling down while a runner is busy is safe: send `SIGTERM`, the
runner stops accepting, finishes what is running (up to 30s), and the worker's
next delivery lands on whichever replica is left.

## Scale to zero: `RUNNER_IDLE_EXIT`

Set `RUNNER_IDLE_EXIT` to a duration (say `10m`) and the runner exits with
status 0 once it has had nothing running and nothing queued for that long. It
is not a health failure: the process logs `runner idle; exiting` and stops so
that the thing supervising it can decide not to start another until work
arrives. Pair it with a supervisor that starts the runner on demand:

- **systemd socket activation.** Declare `RUNNER_LISTEN`'s port as a
  `.socket` unit and the runner as its service. The first connection from the
  worker starts the service; the idle exit stops it; the socket keeps
  listening throughout, so the worker's client sees a slow first answer, not
  a refused connection. Do not set `Restart=always`, which would defeat the
  point; `Restart=on-failure` is right, since the idle exit is status 0.
- **Compose profiles / a single host.** Run the runner under a profile the
  worker host starts when the queue is non-empty (a small cron on
  `recruiting_queue_depth{kind="runner.execute"}` from the worker's metrics
  is enough) and let `RUNNER_IDLE_EXIT` stop it.
- **Nomad / Kubernetes scale-to-zero.** Use the autoscaler rule above with a
  floor of 0. `RUNNER_IDLE_EXIT` is then a belt to the braces: a replica the
  scaler forgot exits on its own. Give the deployment a restart policy of
  `OnFailure` so an idle exit is not immediately undone.

When no runner is up the worker's client cannot connect, the `runner.execute`
job fails that delivery and is retried with the queue's backoff (15s, then
doubling). That is the cold-start latency a candidate can see with a floor of
0: from a few seconds to about a minute depending on how quickly the
supervisor brings a runner up. A floor of 1 with `RUNNER_IDLE_EXIT` unset on
that replica removes it at the cost of one idle process.

## Memory

Each container is limited to the candidate's `mem_mb` plus a fixed 128 MB
headroom (`memoryHeadroomMB` in `internal/runner/server/docker.go`), with
swap disabled. The headroom pays for the harness, which decodes every test's
input, the 256 MB `/tmp` tmpfs a compiler writes into, and a managed
runtime's own overhead; the harness sizes JVM (`-Xmx`) and .NET
(`DOTNET_GCHeapHardLimit`) heaps to three quarters of the candidate's limit,
not of the cgroup, so they cannot grow into it. Everything the runner keeps
from a run is bounded: 4 KiB tails of compile output and stderr, `output_kb`
of stdout, a few MiB for the harness's whole document (over which the run is
a runner error, not the candidate's), and the result cache by bytes.
