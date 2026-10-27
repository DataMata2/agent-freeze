package main

import "golang.org/x/sys/unix"

// getTermios is the ioctl that reads a terminal's settings.
const getTermios = unix.TIOCGETA

// checkPlatform reports what afz needs and this machine lacks: on macOS,
// nothing.
func checkPlatform() error { return nil }
