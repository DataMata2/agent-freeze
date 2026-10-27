package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// getTermios is the ioctl that reads a terminal's settings.
const getTermios = unix.TCGETS

// checkPlatform reports what afz needs and this machine lacks.
func checkPlatform() error {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return errors.New("cgroup v2 not available")
	}
	return nil
}
