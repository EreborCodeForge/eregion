package worker

// State represents the lifecycle state of a pool slot's current generation.
type State string

const (
	StateStarting State = "starting"
	StateIdle     State = "idle"
	StateBusy     State = "busy"
	StateDraining State = "draining"
	StateDead     State = "dead"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
)
