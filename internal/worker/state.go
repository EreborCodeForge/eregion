package worker

// State represents the lifecycle state of a pool slot's current generation.
//
// Operational states (both HTTP and consumer):
//
//	starting → running/ready → draining → stopped|failed
//
// For consumer mode, StateIdle means the OS process is running/ready — not
// broker idle. Without Mithril job telemetry, Eregion does not observe real
// idle/busy job activity. Do not use StateIdle for backlog or job-activity
// decisions on consumer pools. StateBusy is an HTTP request-in-flight state.
type State string

const (
	StateStarting State = "starting"
	StateIdle     State = "idle" // HTTP: ready for request; consumer: process running/ready
	StateBusy     State = "busy" // HTTP only: handling a request
	StateDraining State = "draining"
	StateDead     State = "dead"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
)
