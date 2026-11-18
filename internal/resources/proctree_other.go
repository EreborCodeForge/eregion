//go:build !linux

package resources

// ProcessTreeRSS is unknown outside Linux. A zero result must not be treated as an empty tree.
func ProcessTreeRSS() (uint64, bool) {
	return 0, false
}
