package pressure

import "os"

// Read samples memory pressure (PSI, kernel 4.20+; Normal without it) and CPU load.
func Read() (Reading, error) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return Reading{}, err
	}
	load, err := parseLoadavg(string(raw))
	if err != nil {
		return Reading{}, err
	}
	r := Reading{LoadPerCPU: perCPU(load)}
	if psi, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		if r.Memory, err = parsePSI(string(psi)); err != nil {
			return Reading{}, err
		}
	}
	return r, nil
}
