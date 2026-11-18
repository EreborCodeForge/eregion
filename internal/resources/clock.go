package resources

import "time"

// Clock is the time source for samples and predictions. Tests inject it.
type Clock interface {
	Now() time.Time
}

// SystemClock uses time.Now.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// FuncClock adapts a function to Clock.
type FuncClock func() time.Time

func (f FuncClock) Now() time.Time {
	if f == nil {
		return time.Now()
	}
	return f()
}
