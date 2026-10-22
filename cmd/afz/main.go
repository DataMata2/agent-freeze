//go:build linux

// Command afz freezes idle coding agents to free RAM and thaws them later.
package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/DataMata2/agent-freeze/internal/cli"
	"github.com/DataMata2/agent-freeze/internal/freezer"
)

func main() {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		fmt.Fprintln(os.Stderr, "afz: cgroup v2 not available")
		os.Exit(1)
	}
	api := freezer.New(os.Getuid(), os.Getpid())
	if len(os.Args) > 1 {
		os.Exit(cli.Run(os.Args[1:], api, os.Stdout, os.Stderr))
	}
	// Bare afz opens the TUI on a terminal; otherwise (an agent or a pipe
	// calling it) it lists, so the caller never hangs on a TUI.
	if _, err := unix.IoctlGetTermios(int(os.Stdout.Fd()), unix.TCGETS); err != nil {
		os.Exit(cli.Run([]string{"list"}, api, os.Stdout, os.Stderr))
	}
	if err := runTUI(api); err != nil {
		fmt.Fprintf(os.Stderr, "afz: %s\n", err)
		os.Exit(1)
	}
}
