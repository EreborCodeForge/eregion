//go:build linux

package signals

import (
	"os"
	"strconv"
	"strings"
)

func init() {
	workerCPUSupported = true
	readProc = readProcLinux
}

func readProcLinux(pid int) ProcTimes {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ProcTimes{}
	}
	text := string(raw)
	closeParen := strings.LastIndex(text, ")")
	if closeParen < 0 || closeParen+2 >= len(text) {
		return ProcTimes{}
	}
	fields := strings.Fields(text[closeParen+2:])
	// utime, stime and starttime are fields 14, 15 and 22 of the whole record.
	// After comm they sit at indexes 11, 12 and 19.
	if len(fields) < 20 {
		return ProcTimes{}
	}
	user, err1 := strconv.ParseUint(fields[11], 10, 64)
	sys, err2 := strconv.ParseUint(fields[12], 10, 64)
	start, err3 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start == 0 {
		return ProcTimes{}
	}
	return ProcTimes{User: user, Sys: sys, Start: start, OK: true}
}
