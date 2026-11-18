package worker

import "errors"

// SpawnRequest asks the global gate for permission to create one process.
type SpawnRequest struct {
	Workload    string
	Slot        int
	Generation  uint64
	Replacement bool
}

// SpawnGrant is an idempotent reservation token. ID 0 means no reservation.
type SpawnGrant struct {
	ID         uint64
	Workload   string
	Slot       int
	Generation uint64
}

// SpawnGate is the last check before a process is created.
// Implementations must not call back into the pool.
type SpawnGate interface {
	Admit(SpawnRequest) (SpawnGrant, error)
	Commit(SpawnGrant)
	Release(SpawnGrant)
	ReleaseExit(SpawnGrant)
}

type admissionDenial interface {
	AdmissionDenied() bool
}

// IsAdmissionDenial reports a refused spawn that must not be treated as a crash.
func IsAdmissionDenial(err error) bool {
	var denial admissionDenial
	return err != nil && errors.As(err, &denial) && denial.AdmissionDenied()
}
