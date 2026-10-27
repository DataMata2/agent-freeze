package pressure

import "golang.org/x/sys/unix"

// Read samples memory pressure and CPU load.
func Read() (Reading, error) {
	level, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return Reading{}, err
	}
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return Reading{}, err
	}
	load, err := parseDarwinLoadavg(raw)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Memory: darwinLevel(level), LoadPerCPU: perCPU(load)}, nil
}
