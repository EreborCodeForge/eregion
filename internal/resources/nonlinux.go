//go:build !linux

package resources

func (d SystemDetector) platformDetect(r *RuntimeResources) {
	// Non-Linux: no cgroups. AvailableCPUs filled by applyCPUFallbacks.
	r.Environment = EnvUnknown
	r.CPUQuotaDetected = false
	r.CPUSetDetected = false
}
