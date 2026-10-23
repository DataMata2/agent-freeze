package procfs

// Process is one process of a snapshot: a /proc/<pid> entry on Linux, a
// kernel process table entry on macOS.
type Process struct {
	PID  int
	PPID int
	// StartTime is /proc/<pid>/stat field 22 (clock ticks since boot) on
	// Linux and microseconds since the Unix epoch on macOS.
	StartTime uint64
	Comm      string
	Exe       string   // readlink(/proc/<pid>/exe) with " (deleted)" stripped; "" if unreadable
	Args      []string // cmdline split on NUL, trailing empty element dropped; nil for kernel threads
	Cwd       string   // readlink(/proc/<pid>/cwd); "" if unreadable
	RSS       uint64   // VmRSS * 1024; 0 if absent
	Swap      uint64   // VmSwap * 1024; 0 if absent
	Cgroup    string   // path from the "0::" line of /proc/<pid>/cgroup; "" if none

	// Read on macOS only, where the freezer works with signals and job
	// control instead of cgroups; zero on Linux.
	PGID  int
	UID   int
	State byte   // 'T' stopped, 'Z' zombie, 'R' otherwise (macOS does not tell sleeping apart)
	TTY   string // controlling terminal, e.g. /dev/ttys003; "" if none
	// Foreground: the process group owns its terminal (a shell's foreground
	// job, not one started with "&").
	Foreground bool
}

// MemInfo holds system memory figures in bytes.
type MemInfo struct{ MemTotal, MemAvailable, SwapTotal, SwapFree uint64 }
