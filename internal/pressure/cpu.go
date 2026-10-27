package pressure

import (
	"fmt"
	"strconv"
	"strings"
)

// GroupCPU sums perPID over pids.
func GroupCPU(perPID map[int]float64, pids []int) float64 {
	var sum float64
	for _, pid := range pids {
		sum += perPID[pid]
	}
	return sum
}

// parsePS reads `ps -A -o pid=,%cpu=` output.
func parsePS(text string) map[int]float64 {
	out := map[int]float64{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		pct, err2 := strconv.ParseFloat(fields[1], 64)
		if err1 == nil && err2 == nil {
			out[pid] = pct
		}
	}
	return out
}

// comm may hold spaces and parens, so fields count from the last ')'.
func parseStatTicks(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, fmt.Errorf("stat: no comm in %q", stat)
	}
	fields := strings.Fields(stat[i+1:]) // fields[0] is field 3 (state)
	if len(fields) < 13 {
		return 0, fmt.Errorf("stat: %d fields after comm, want 13+", len(fields))
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}
