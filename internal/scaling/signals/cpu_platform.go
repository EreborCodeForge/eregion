package signals

// Linux exports utime/stime in USER_HZ, which is 100 on every supported architecture.
const linuxTicksPerSecond = 100

var (
	workerCPUSupported bool
	readProc           func(int) ProcTimes
)

// WorkerCPUSupported reports a permanent platform capability, not a fresh sample.
func WorkerCPUSupported() bool { return workerCPUSupported }

// GlobalCPUSupported is true where the existing cgroup sampler can run.
func GlobalCPUSupported() bool { return workerCPUSupported }

// OSReader reads /proc on Linux and refuses the measurement elsewhere.
type OSReader struct{}

func (OSReader) Read(pid int) ProcTimes {
	if readProc == nil || pid <= 0 {
		return ProcTimes{}
	}
	return readProc(pid)
}

func (OSReader) ticksPerSecond() float64 {
	if !workerCPUSupported {
		return 0
	}
	return linuxTicksPerSecond
}
