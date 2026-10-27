package pressure

import "os/exec"

// Sampler reports each process's recent CPU use in percent of one core.
type Sampler struct{}

// Sample returns ps's %cpu per process, a decaying average over about the last minute.
func (s *Sampler) Sample() map[int]float64 {
	out, err := exec.Command("ps", "-A", "-o", "pid=,%cpu=").Output()
	if err != nil {
		return map[int]float64{}
	}
	return parsePS(string(out))
}
