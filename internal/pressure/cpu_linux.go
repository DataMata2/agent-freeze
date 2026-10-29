package pressure

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// USER_HZ, the unit of /proc/<pid>/stat CPU times: 100 on every mainstream Linux.
const clockTicks = 100

// Sampler reports CPU use in % of one core since the previous Sample (zeros at first).
type Sampler struct {
	prev   map[int]uint64 // utime+stime ticks per pid
	prevAt time.Time
}

// Sample returns each process's CPU use since the previous Sample.
func (s *Sampler) Sample() map[int]float64 {
	now := time.Now()
	cur := map[int]uint64{}
	paths, _ := filepath.Glob("/proc/[0-9]*/stat")
	for _, p := range paths {
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(p)))
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if ticks, err := parseStatTicks(string(raw)); err == nil {
			cur[pid] = ticks
		}
	}
	out := make(map[int]float64, len(cur))
	elapsed := now.Sub(s.prevAt).Seconds()
	for pid, ticks := range cur {
		if old, ok := s.prev[pid]; ok && elapsed > 0 && ticks >= old {
			out[pid] = float64(ticks-old) / clockTicks / elapsed * 100
		} else {
			out[pid] = 0
		}
	}
	s.prev, s.prevAt = cur, now
	return out
}
