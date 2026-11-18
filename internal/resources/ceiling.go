package resources

// ApplyLocalCeiling tightens a known cgroup limit, or installs an explicit
// local ceiling only when managed-process consumption of that same scope is known.
// It never treats host RAM as available and never mixes cgroup current with an unrelated ceiling.
func ApplyLocalCeiling(sample Sample, hardLimitMB int, managedBytes uint64, managedKnown bool) Sample {
	if hardLimitMB <= 0 {
		return sample
	}
	local := uint64(hardLimitMB) * 1024 * 1024
	if sample.MemoryLimitValid == ValidityKnown && sample.MemoryCurrentValid == ValidityKnown &&
		(sample.Source == SourceCgroupV1 || sample.Source == SourceCgroupV2) && sample.Scope == "cgroup" {
		if local < sample.MemoryLimitBytes {
			sample.MemoryLimitBytes = local
		}
		return sample
	}
	if sample.MemoryLimitValid == ValidityKnown {
		return sample
	}
	if !managedKnown {
		return sample
	}
	sample.MemoryLimitBytes = local
	sample.MemoryLimitValid = ValidityKnown
	sample.MemoryCurrentBytes = managedBytes
	sample.MemoryCurrentValid = ValidityKnown
	sample.Scope = "process_tree"
	if sample.Source == SourceUnknown || sample.Source == "" {
		sample.Source = SourceHost
	}
	return sample
}
