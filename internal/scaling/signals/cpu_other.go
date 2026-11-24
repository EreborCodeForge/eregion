//go:build !linux

package signals

func init() {
	workerCPUSupported = false
	readProc = func(int) ProcTimes { return ProcTimes{} }
}
