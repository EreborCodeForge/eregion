//go:build linux

package resources

func (d SystemDetector) platformDetect(r *RuntimeResources) {
	ApplyCgroupDetectionLogged(d.reader(), d.cgroupRoot(), r, d)
	if r.Environment == "" || r.Environment == EnvUnknown {
		if r.AvailableCPUs > 0 && (r.CPUQuotaDetected || r.CPUSetDetected) {
			// already set by ApplyCgroupDetection
			return
		}
		r.Environment = EnvHost
	}
}
