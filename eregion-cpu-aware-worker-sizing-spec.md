# Eregion — CPU-Aware Resource Detection & Worker Sizing Advisor Specification

**Repository:** `EreborCodeForge/eregion`  
**Target:** `main`  
**Document type:** technical implementation specification  
**Scope:** runtime resource detection, startup diagnostics, worker sizing recommendation, and observability  
**Priority:** incremental / non-breaking  
**Compatibility:** must preserve current `eregion.yaml` semantics and EREGION/1

---

# 1. Purpose

This specification defines how Eregion should become aware of the CPU and memory resources actually available to the runtime environment, especially when running inside:

- Docker;
- Kubernetes;
- Linux containers using cgroups;
- virtual machines;
- bare metal.

The primary objective is to improve operational visibility and help users avoid clearly oversized or undersized PHP worker pools.

This feature must NOT initially override or silently change:

```yaml
workers:
  count: N
```

The configured worker count remains authoritative.

The first implementation must act as:

```text
resource detector
        +
startup diagnostics
        +
metrics
        +
worker sizing recommendation
```

and not as an automatic resource controller.

---

# 2. Problem

Eregion currently starts the number of workers configured by:

```yaml
workers:
  count: N
```

or derived from runtime defaults.

However, the application can run under environments where the host CPU count is very different from the CPU actually available to the process.

Example:

```text
physical host:
32 CPUs

Kubernetes pod CPU limit:
2 CPUs

workers.count:
32
```

Starting 32 persistent PHP processes against 2 available CPUs may result in:

- excessive context switching;
- higher memory usage;
- increased scheduler contention;
- degraded p95/p99 latency;
- no meaningful throughput gain;
- more unstable saturation behavior.

At the same time, automatically reducing the worker count would also be unsafe because some applications are highly I/O-bound and intentionally use more workers than CPU cores.

Therefore, Eregion must first **observe and advise**, not enforce.

---

# 3. Design principle

The configuration remains sovereign.

This means:

```text
configured workers = actual workers started
```

The resource-aware subsystem may:

- detect;
- calculate;
- classify;
- log;
- expose metrics;
- recommend.

It must not, in the first version:

- reduce worker count;
- increase worker count;
- pin workers to CPU cores;
- modify `GOMAXPROCS`;
- alter Kubernetes limits;
- dynamically resize the pool.

---

# 4. Core architecture

Introduce two independent concepts.

```text
Resource Detection
        ↓
RuntimeResources
        ↓
Worker Sizing Analysis
        ↓
WorkerSizingRecommendation
        ↓
Logs + Metrics
```

Suggested packages:

```text
internal/
  resources/
    detect.go
    linux.go
    cgroup.go
    resources.go

  sizing/
    advisor.go
    classification.go
```

Avoid coupling this directly to:

```text
worker.Pool
dispatcher
protocol
```

The worker pool should receive already-resolved configuration as it does today.

---

# 5. Runtime resource model

Suggested domain model:

```go
type RuntimeResources struct {
    LogicalCPUs       int
    AvailableCPUs     float64
    GOMAXPROCS        int
    CPUQuotaDetected  bool

    MemoryLimitBytes  uint64
    MemoryLimitKnown  bool

    Environment       string
}
```

Possible `Environment` values:

```text
bare-metal
container
cgroup-v1
cgroup-v2
unknown
```

Do not rely on this field for logic.

It is diagnostic metadata only.

---

# 6. CPU values

Eregion should distinguish three concepts.

## 6.1 Logical CPUs

Use:

```go
runtime.NumCPU()
```

This represents the CPUs visible to the Go runtime.

Example:

```text
logical_cpus=16
```

---

## 6.2 GOMAXPROCS

Use:

```go
runtime.GOMAXPROCS(0)
```

Expose current value.

Example:

```text
gomaxprocs=2
```

Do not modify it automatically in this feature.

---

## 6.3 Available CPU quota

Detect the effective CPU quota exposed by cgroups when available.

Examples:

```text
2 CPU
0.5 CPU
1.75 CPU
```

This must be represented as:

```go
float64
```

because Kubernetes allows fractional CPU quotas.

Example:

```yaml
resources:
  limits:
    cpu: "500m"
```

should produce approximately:

```text
available_cpu=0.5
```

---

# 7. CPU detection precedence

Recommended precedence:

```text
1. cgroup v2 CPU quota
2. cgroup v1 CPU quota
3. GOMAXPROCS
4. runtime.NumCPU()
```

Conceptually:

```go
availableCPU := detectCgroupCPU()

if unavailable {
    availableCPU = float64(runtime.GOMAXPROCS(0))
}

if availableCPU <= 0 {
    availableCPU = float64(runtime.NumCPU())
}
```

---

# 8. cgroup v2 CPU detection

On Linux, inspect:

```text
/sys/fs/cgroup/cpu.max
```

Typical values:

```text
200000 100000
```

Meaning:

```text
quota = 200000
period = 100000
availableCPU = quota / period = 2
```

Unlimited example:

```text
max 100000
```

means there is no explicit CPU quota.

Do not treat `max` as zero.

Fall back to the next detection strategy.

---

# 9. cgroup v1 CPU detection

Possible files:

```text
/sys/fs/cgroup/cpu/cpu.cfs_quota_us
/sys/fs/cgroup/cpu/cpu.cfs_period_us
```

Calculate:

```text
availableCPU =
cpu.cfs_quota_us / cpu.cfs_period_us
```

If quota is:

```text
-1
```

treat as unlimited.

---

# 10. CPU set awareness

Optional but recommended.

Containers may be restricted using cpuset rather than CPU quota.

Relevant files may include:

```text
/sys/fs/cgroup/cpuset.cpus.effective
/sys/fs/cgroup/cpuset/cpuset.cpus
```

Example:

```text
0-3
```

means:

```text
4 CPUs
```

Effective available CPU should use the most restrictive detected value.

Example:

```text
quota = 8
cpuset = 4 CPUs

available = 4
```

Do not overcomplicate initial implementation if cpuset support creates portability risk.

This can be implemented as a second step.

---

# 11. Non-Linux behavior

On non-Linux systems:

```text
AvailableCPUs = GOMAXPROCS
```

Fallback:

```text
AvailableCPUs = runtime.NumCPU()
```

No startup failure should happen because cgroups are unavailable.

Resource detection must always degrade gracefully.

---

# 12. Memory limit detection

This feature is primarily CPU-focused, but memory information is highly useful because each PHP worker consumes independent process memory.

Detect container memory limit when possible.

cgroup v2:

```text
/sys/fs/cgroup/memory.max
```

cgroup v1:

```text
/sys/fs/cgroup/memory/memory.limit_in_bytes
```

Represent:

```go
MemoryLimitBytes uint64
MemoryLimitKnown bool
```

If unlimited or unknown:

```text
MemoryLimitKnown=false
```

---

# 13. Resource detector interface

Suggested interface:

```go
type Detector interface {
    Detect() RuntimeResources
}
```

Default implementation:

```go
type SystemDetector struct{}
```

Resource detection should not return fatal errors for missing cgroup files.

Instead, diagnostics may be logged at debug level.

---

# 14. Worker sizing model

Introduce:

```go
type WorkerSizing struct {
    ConfiguredWorkers     int
    AvailableCPUs         float64
    WorkersPerCPU         float64

    RecommendedWorkers    int
    RecommendedMin        int
    RecommendedMax        int

    Classification        string
    Warning               bool
    Reason                string
}
```

---

# 15. Initial sizing heuristic

The initial recommendation should remain intentionally conservative.

Default conceptual profile:

```text
recommended = available CPU × 2
```

Reason:

A typical PHP API often contains both:

- CPU work;
- blocking I/O;
- database calls;
- network calls;
- cache calls.

Therefore `2 workers / CPU` is a reasonable initial recommendation, but not an enforcement rule.

---

# 16. Recommended range

Instead of presenting only one exact number, expose a range.

Suggested baseline:

```text
recommended_min = ceil(availableCPU × 1)
recommended     = ceil(availableCPU × 2)
recommended_max = ceil(availableCPU × 4)
```

Example:

```text
availableCPU = 2

min         = 2
recommended = 4
max         = 8
```

Interpretation:

```text
2 workers -> conservative / CPU-heavy
4 workers -> balanced default
8 workers -> I/O-heavy upper guidance
```

---

# 17. Fractional CPU

Example:

```text
availableCPU = 0.5
```

Calculation must never recommend zero workers.

Use:

```go
max(1, ceil(...))
```

Example:

```text
min = 1
recommended = 1
max = 2
```

---

# 18. Worker-per-CPU ratio

Calculate:

```text
workers_per_cpu =
configured_workers / available_cpu
```

Example:

```text
workers=16
availableCPU=2

workers_per_cpu=8
```

---

# 19. Initial classification

Suggested classification:

| Workers per CPU | Classification |
|---:|---|
| `<= 1.0` | `cpu_conservative` |
| `> 1.0 && <= 2.0` | `balanced` |
| `> 2.0 && <= 4.0` | `io_optimized` |
| `> 4.0 && <= 8.0` | `high_oversubscription` |
| `> 8.0` | `extreme_oversubscription` |

These classifications are diagnostics, not correctness judgments.

---

# 20. Warning policy

Emit WARN when:

```text
workers_per_cpu > 8
```

or configured workers are significantly above the recommended range.

Example:

```text
availableCPU=1
workers=16
```

Log:

```text
WARN worker pool may be oversized for available CPU
```

Do not fail startup.

---

# 21. INFO policy

For normal configurations, log at INFO.

Example:

```text
INFO runtime resources detected
cpu_logical=16
cpu_available=2
gomaxprocs=2
memory_limit_mb=1024
environment=cgroup-v2
```

Then:

```text
INFO worker pool sizing
workers_configured=4
workers_per_cpu=2
workers_recommended=4
workers_recommended_min=2
workers_recommended_max=8
classification=balanced
```

---

# 22. Oversized warning example

Example:

```text
WARN worker pool may be oversized for available CPU
cpu_available=1
workers_configured=32
workers_per_cpu=32
workers_recommended=2
workers_recommended_max=4
classification=extreme_oversubscription
```

---

# 23. Undersized diagnostic

Do not WARN by default for:

```text
workers_per_cpu < 1
```

Some workloads are intentionally CPU-heavy.

Use INFO or DEBUG:

```text
worker pool is configured conservatively for available CPU
```

---

# 24. Startup integration

Resource detection should happen before the pool starts.

Suggested server startup flow:

```text
load config
validate config
detect runtime resources
analyze worker sizing
log runtime resources
log sizing recommendation
start workers
start HTTP server
```

Do not delay worker startup with expensive probing.

Detection must be synchronous and fast.

---

# 25. Suggested component

```go
type Advisor struct{}

func (Advisor) Analyze(
    resources resources.RuntimeResources,
    configuredWorkers int,
) WorkerSizing
```

Keep it pure and deterministic.

This makes it easy to test.

---

# 26. No configuration changes in v1

Do NOT add yet:

```yaml
workers:
  sizing_mode:
  cpu_guard:
  workers_per_cpu:
  auto_size:
```

The first implementation must require no YAML change.

This prevents unnecessary public API expansion.

---

# 27. Metrics

Expose the detected resources.

Suggested metrics:

```text
eregion_runtime_cpu_logical
eregion_runtime_cpu_available
eregion_runtime_gomaxprocs
eregion_runtime_memory_limit_bytes
```

---

# 28. Worker sizing metrics

Add:

```text
eregion_workers_configured
eregion_workers_per_cpu
eregion_workers_recommended
eregion_workers_recommended_min
eregion_workers_recommended_max
```

Some of these overlap existing worker metrics.

Avoid duplicate metrics if one already represents the same semantic value.

For example:

```text
eregion_workers_desired
```

may remain the authoritative configured count.

In that case do NOT add:

```text
eregion_workers_configured
```

Use the existing metric.

---

# 29. Recommended metric set

Prefer:

```text
eregion_runtime_cpu_available
eregion_runtime_cpu_logical
eregion_runtime_gomaxprocs

eregion_workers_desired
eregion_workers_per_cpu
eregion_workers_recommended
eregion_workers_recommended_min
eregion_workers_recommended_max
```

---

# 30. Metric stability

Resource metrics should generally remain constant for the lifetime of the process.

Do not re-read cgroups on every `/metrics` request.

Detect once at startup and cache.

---

# 31. Future dynamic environments

CPU quotas can theoretically change during runtime.

Do not support dynamic quota refresh initially.

Possible future feature:

```text
periodic resource refresh
```

but this is out of scope.

---

# 32. Logging and privacy

Do not log:

- pod names unless already available;
- container IDs;
- hostnames specifically for this subsystem;
- cgroup raw paths;
- environment variables.

Only log resource values and detection mode.

---

# 33. Tests — CPU sizing advisor

Pure unit tests must cover:

```text
availableCPU = 1
workers = 1
=> conservative
```

```text
availableCPU = 1
workers = 2
=> balanced
```

```text
availableCPU = 2
workers = 4
=> balanced
```

```text
availableCPU = 2
workers = 8
=> io_optimized
```

```text
availableCPU = 1
workers = 16
=> extreme_oversubscription + warning
```

---

# 34. Fractional CPU tests

```text
availableCPU = 0.5
workers = 1
```

Ensure:

```text
recommended >= 1
```

No division by zero.

---

# 35. Missing CPU information

If detection somehow returns:

```text
availableCPU <= 0
```

fallback to:

```text
GOMAXPROCS
```

then:

```text
runtime.NumCPU()
```

Finally:

```text
1
```

The system must always produce a valid recommendation.

---

# 36. cgroup v2 parser tests

Given:

```text
200000 100000
```

expect:

```text
2
```

Given:

```text
50000 100000
```

expect:

```text
0.5
```

Given:

```text
max 100000
```

expect:

```text
quota unavailable/unlimited
```

---

# 37. cgroup v1 parser tests

Given:

```text
quota=200000
period=100000
```

expect:

```text
2
```

Given:

```text
quota=-1
```

expect:

```text
unlimited
```

---

# 38. Resource detector testability

Do not hardcode direct filesystem reads throughout the package.

Introduce a small file reader abstraction or inject root path for tests.

Example:

```go
type FileReader interface {
    ReadFile(path string) ([]byte, error)
}
```

or:

```go
type Detector struct {
    CgroupRoot string
}
```

Prefer the simpler implementation.

---

# 39. Performance considerations

Resource detection runs once.

Therefore micro-optimization is unnecessary.

Still avoid:

- spawning shell commands;
- invoking `nproc`;
- executing Docker CLI;
- calling Kubernetes APIs;
- reading `/proc` repeatedly.

Use Go standard library and cgroup files directly.

---

# 40. Kubernetes behavior

Example pod:

```yaml
resources:
  requests:
    cpu: "500m"
  limits:
    cpu: "2"
```

Eregion should preferably detect:

```text
availableCPU=2
```

because CPU limit defines the maximum schedulable CPU quota.

It should not infer worker sizing from `requests.cpu`.

---

# 41. No CPU affinity

Do not implement:

```text
worker-1 -> CPU0
worker-2 -> CPU1
```

CPU scheduling remains the responsibility of:

- Linux scheduler;
- container runtime;
- Kubernetes/cgroups.

---

# 42. No virtual CPU abstraction

Do not create any Eregion concept of:

```text
virtual core
worker core
logical worker CPU
```

Workers are OS processes.

The advisor simply analyzes the relationship between:

```text
available CPU
and
worker process count
```

---

# 43. Relationship with PHP worker memory

Because each worker is a separate PHP process, worker count also affects memory.

If:

```text
memory limit = 1 GiB
workers = 16
```

then even moderate PHP process memory can become a constraint.

Future advisor may combine:

```text
CPU sizing
+
memory sizing
```

This is out of scope for automatic recommendation v1.

---

# 44. Optional memory diagnostic

A lightweight informational calculation is acceptable.

If:

```yaml
workers:
  memory_limit_mb: 256
  count: 8
```

the theoretical maximum cooperative memory envelope is:

```text
8 × 256 MiB
```

Do NOT treat this as actual RSS usage.

Do NOT fail startup based on this.

Possible log:

```text
worker_memory_configured_upper_bound_mb=2048
```

Only add if useful and clearly documented.

---

# 45. Future Worker Sizing Advisor v2

After benchmark data exists, Eregion may use runtime behavior to improve recommendations.

Potential inputs:

```text
CPU utilization
workers busy ratio
queue wait
worker execution duration
HTTP throughput
p95
p99
503 rate
```

---

# 46. Example future logic

```text
CPU < 50%
workers busy ~100%
queue growing
```

Possible interpretation:

```text
I/O-bound
more workers may improve throughput
```

---

```text
CPU > 95%
workers busy ~100%
queue growing
```

Possible interpretation:

```text
CPU saturated
more workers are unlikely to help
```

---

```text
CPU > 95%
workers_per_cpu > 8
p99 increasing
```

Possible interpretation:

```text
pool likely oversized
```

---

# 47. Important — no autosizing yet

Even after runtime analysis exists, recommendation should come before automation.

Future order:

```text
v1
detect + log + metrics

v2
dynamic recommendation

v3
optional explicit auto-sizing mode
```

No v3 behavior should be introduced in this implementation.

---

# 48. Suggested startup output

Normal configuration:

```text
INFO runtime resources detected
cpu_logical=16
cpu_available=2
gomaxprocs=2
cpu_quota=true
memory_limit_mb=1024
environment=cgroup-v2

INFO worker pool sizing
workers_configured=4
workers_per_cpu=2
workers_recommended=4
workers_recommended_min=2
workers_recommended_max=8
classification=balanced
```

---

# 49. Extreme configuration

```text
WARN worker pool may be oversized for available CPU
cpu_available=1
workers_configured=32
workers_per_cpu=32
workers_recommended=2
workers_recommended_max=4
classification=extreme_oversubscription
```

Startup must continue.

---

# 50. Bare metal example

Machine:

```text
8 CPUs
GOMAXPROCS=8
```

Config:

```yaml
workers:
  count: 8
```

Result:

```text
availableCPU=8
workers_per_cpu=1
classification=cpu_conservative
recommended=16
```

This is not an error.

Do not warn.

---

# 51. Queue relationship

The advisor must not modify:

```yaml
queue:
  capacity:
```

Queue capacity remains independently resolved.

However, startup log may include:

```text
queue_capacity=64
max_admitted=workers+queue
```

Optional.

---

# 52. Default worker count

Do NOT change the current default worker count in the same PR unless benchmark evidence supports it.

This feature must first provide visibility.

A future separate decision may consider changing:

```go
runtime.NumCPU()
```

to:

```text
availableCPU × 2
```

but only after stress testing.

---

# 53. Benchmark plan after implementation

Test worker configurations:

```text
CPU × 1
CPU × 2
CPU × 4
CPU × 8
```

Example with 2 CPU:

```text
2 workers
4 workers
8 workers
16 workers
```

Observe:

```text
RPS
CPU
RSS
p50
p95
p99
queue_wait
worker_duration
503
504
context switches if available
```

---

# 54. Expected benchmark behavior

Typical mixed API workload may show:

```text
CPU×1 -> underutilized during I/O
CPU×2 -> best balance
CPU×4 -> potentially more throughput
CPU×8 -> diminishing returns / latency penalty
```

This is a hypothesis, not a guaranteed result.

---

# 55. Integration points

Likely integration points:

```text
cmd/eregion/main.go
or
internal/server/server.go
```

Recommended:

detect and analyze once during server initialization/startup.

Then inject immutable results into telemetry.

Example:

```go
resources := resources.Detect()
sizing := sizing.Analyze(
    resources,
    cfg.Workers.Count,
)
```

---

# 56. Telemetry constructor

Possible extension:

```go
telemetry.NewRegistry(
    cfg,
    pool,
    resources,
    sizing,
)
```

Avoid global variables.

---

# 57. Logging helper

Possible:

```go
func LogRuntimeResources(
    logger *slog.Logger,
    r RuntimeResources,
    s WorkerSizing,
)
```

Keep formatting centralized.

---

# 58. Error handling

Resource detection failure must NEVER prevent Eregion from starting unless an internal programming invariant is broken.

Fallback behavior:

```text
WARN resource detection incomplete
fallback_cpu=...
```

Then continue.

---

# 59. No external dependencies

Prefer standard library only.

Do not add Kubernetes SDK.

Do not depend on Docker APIs.

Do not require privileged container access.

---

# 60. Security

Only read standard local cgroup metadata.

Do not:

- inspect unrelated host process data;
- enumerate other cgroups;
- inspect `/proc/<pid>` of unrelated processes;
- use privileged system calls.

---

# 61. Definition of Done — phase 1

Phase 1 is complete when:

- [ ] logical CPU is detected;
- [ ] GOMAXPROCS is detected;
- [ ] Linux cgroup v2 CPU quota is supported;
- [ ] Linux cgroup v1 CPU quota is supported or explicitly deferred;
- [ ] fallback behavior works outside containers;
- [ ] available CPU supports fractional values;
- [ ] sizing advisor is implemented as a pure function;
- [ ] workers-per-CPU is calculated;
- [ ] recommendation min/default/max is calculated;
- [ ] oversized configurations produce WARN;
- [ ] normal configurations produce INFO;
- [ ] configured worker count is NOT modified;
- [ ] startup continues regardless of recommendation;
- [ ] metrics expose CPU/resource sizing;
- [ ] unit tests cover parsing and recommendations;
- [ ] README documents the behavior.

---

# 62. Definition of Done — optional phase 1.1

Optional:

- [ ] memory cgroup detection;
- [ ] cpuset detection;
- [ ] memory metrics;
- [ ] worker memory envelope diagnostic.

These are not required for the first usable implementation.

---

# 63. Explicit non-goals

Do NOT implement:

- [ ] dynamic worker pool resizing;
- [ ] worker CPU pinning;
- [ ] changing cgroup quotas;
- [ ] Kubernetes API calls;
- [ ] Docker API calls;
- [ ] CPU throttling inside Eregion;
- [ ] virtual CPUs;
- [ ] automatic `GOMAXPROCS` changes;
- [ ] automatic YAML rewriting;
- [ ] startup failure because worker count is above recommendation.

---

# 64. Suggested PR split

## PR 1

```text
feat: detect runtime CPU resources
```

Includes:

- resource model;
- cgroup detection;
- tests;
- startup log.

---

## PR 2

```text
feat: add worker sizing advisor
```

Includes:

- sizing model;
- recommendation logic;
- warning classification;
- tests.

---

## PR 3

```text
feat: expose runtime sizing telemetry
```

Includes:

- metrics;
- documentation;
- optional memory detection.

A single PR is acceptable if changes remain small and reviewable.

---

# 65. Acceptance examples

## Example A

```text
availableCPU=1
workers=2
```

Expected:

```text
classification=balanced
warning=false
recommended=2
```

---

## Example B

```text
availableCPU=2
workers=8
```

Expected approximately:

```text
workers_per_cpu=4
classification=io_optimized
warning=false
recommended=4
recommended_max=8
```

---

## Example C

```text
availableCPU=1
workers=16
```

Expected:

```text
workers_per_cpu=16
classification=extreme_oversubscription
warning=true
recommended=2
recommended_max=4
```

Eregion still starts 16 workers.

---

# 66. Architectural rule

The resource subsystem observes the environment.

The sizing subsystem interprets those resources.

The worker pool executes configured intent.

These responsibilities must remain separate.

```text
Resources
   ↓
Sizing Advisor
   ↓
Diagnostics

Config
   ↓
Worker Pool
```

Do not create:

```text
Sizing Advisor
   ↓
silently mutates Config
```

---

# 67. Final expected behavior

At startup, an Eregion operator should immediately understand:

```text
How much CPU does this runtime actually have?
How many PHP workers were configured?
How many workers exist per available CPU?
Is this configuration conservative, balanced, or highly oversubscribed?
What range does Eregion recommend as a benchmark starting point?
```

Without changing application behavior.

---

# 68. Long-term goal

This feature creates the foundation for a future adaptive performance advisor.

The eventual Eregion runtime may be capable of saying:

```text
Current:
16 workers
2 available CPU
CPU utilization: 99%
queue wait p95: 420ms
worker execution p95: 75ms

Recommendation:
benchmark 8 workers
```

But such adaptive recommendations must be based on real benchmark/runtime data and are explicitly outside this first implementation.

---

# 69. Final rule for the implementing agent

Do not optimize by assumption.

Implement:

```text
detect
measure
recommend
observe
```

before introducing:

```text
control
enforcement
automatic tuning
```

The first version succeeds when Eregion becomes resource-aware without becoming unpredictable.
