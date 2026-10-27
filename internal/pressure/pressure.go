// Package pressure reads how hard the system is pressed for memory and CPU.
package pressure

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// Level is how much the system wants memory back.
type Level int

const (
	Normal Level = iota
	Warn
	Critical
)

func (l Level) String() string {
	switch l {
	case Warn:
		return "warn"
	case Critical:
		return "critical"
	}
	return "normal"
}

// Reading is one sample of system pressure.
type Reading struct {
	Memory     Level
	LoadPerCPU float64 // 1-minute load average divided by the CPU count
}

// PSI avg10: % of the last 10s in which some task, or every task, waited for memory.
const (
	psiSomeWarn     = 10.0
	psiFullCritical = 10.0
)

// parsePSI reads /proc/pressure/memory.
func parsePSI(text string) (Level, error) {
	avg10 := map[string]float64{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, ok := strings.CutPrefix(fields[1], "avg10=")
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return Normal, fmt.Errorf("psi: %w", err)
		}
		avg10[fields[0]] = f
	}
	some, ok := avg10["some"]
	if !ok {
		return Normal, fmt.Errorf("psi: no \"some\" line in %q", text)
	}
	switch {
	case avg10["full"] >= psiFullCritical:
		return Critical, nil
	case some >= psiSomeWarn:
		return Warn, nil
	}
	return Normal, nil
}

// parseLoadavg reads the 1-minute load from /proc/loadavg.
func parseLoadavg(text string) (float64, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return 0, fmt.Errorf("loadavg: empty")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// darwinLevel maps kern.memorystatus_vm_pressure_level (1, 2, 4).
func darwinLevel(v uint32) Level {
	switch v {
	case 2:
		return Warn
	case 4:
		return Critical
	}
	return Normal
}

// vm.loadavg is struct loadavg { fixpt_t ldavg[3]; long fscale; }.
func parseDarwinLoadavg(raw []byte) (float64, error) {
	if len(raw) < 24 {
		return 0, fmt.Errorf("vm.loadavg: %d bytes, want 24", len(raw))
	}
	scale := binary.LittleEndian.Uint64(raw[16:24])
	if scale == 0 {
		return 0, fmt.Errorf("vm.loadavg: zero fscale")
	}
	return float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(scale), nil
}

func perCPU(load float64) float64 { return load / float64(runtime.NumCPU()) }
