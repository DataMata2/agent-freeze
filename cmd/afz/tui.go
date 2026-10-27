//go:build linux || darwin

package main

import (
	"github.com/DataMata2/agent-freeze/internal/cli"
	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/tui"
)

// runTUI opens the interactive TUI.
func runTUI(api freezer.API) error { return tui.Run(api, cli.SetAuto) }
