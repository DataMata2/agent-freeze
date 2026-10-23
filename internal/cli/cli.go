// Package cli implements afz's command-line front end over freezer.API.
package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/DataMata2/agent-freeze/internal/discover"
	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/human"
	"github.com/DataMata2/agent-freeze/internal/prog"
)

// Version is the afz version, set at build time with -ldflags -X.
var Version = "dev"

// usageHint and helpText take the command name (prog.Name) as %[1]s.
const usageHint = "usage: %[1]s {list|freeze|thaw|status|rename|version|help} [flags] [selector...]; see '%[1]s help'"

const helpText = `%[1]s - freeze idle coding agents to free RAM

usage: %[1]s <command> [flags] [selector...]

commands:
  list      show agent groups (ID NAME STATE STATUS RAM SWAP PROCS CWD)
  freeze    freeze the selected groups and push their memory to swap
  thaw      thaw the selected groups
  status    show frozen totals and system memory
  rename    rename this command, e.g. '%[1]s rename chill'
  version   print the version
  help      show this help (also -h, --help)

flags:
  --json             machine-readable output (all commands but help)
  --all              freeze/thaw: every eligible group instead of selectors
  --include-daemons  freeze --all: include agent daemons
  --yes              freeze: proceed even if free swap is too small
  --force-self       freeze: allow freezing the group %[1]s runs in
  --all-procs        list: print each group's PIDs

A selector is a group ID, a group key, or a case-insensitive substring of
the name. An exact ID or key wins; a substring matching several is an error.
`

// validFlags lists the flags each command accepts.
var validFlags = map[string][]string{
	"list":    {"--json", "--all-procs"},
	"freeze":  {"--json", "--all", "--include-daemons", "--yes", "--force-self"},
	"thaw":    {"--json", "--all"},
	"status":  {"--json"},
	"rename":  {"--json"},
	"version": {"--json"},
	"help":    {},
	"-h":      {},
	"--help":  {},
}

// takesSelectors reports which commands accept positional arguments.
var takesSelectors = map[string]bool{"freeze": true, "thaw": true, "rename": true}

type opts struct {
	json, all, includeDaemons, yes, forceSelf, allProcs bool
}

// usageError is a bad invocation (exit 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// Run executes afz with args (excluding the program name) and returns the
// process exit code: 0 success, 1 operation failure, 2 usage error.
func Run(args []string, api freezer.API, stdout, stderr io.Writer) int {
	o := &output{stdout: stdout, stderr: stderr, json: slices.Contains(args, "--json")}
	err := dispatch(args, api, o)
	if err == nil {
		return o.code
	}
	if ue, ok := errors.AsType[usageError](err); ok {
		if o.json {
			o.writeJSON(map[string]string{"error": ue.msg})
		} else {
			fmt.Fprintf(stderr, "%s: %s\n"+usageHint+"\n", prog.Name, ue.msg)
		}
		return 2
	}
	o.fail(err)
	return 1
}

func dispatch(args []string, api freezer.API, o *output) error {
	if len(args) == 0 {
		return usagef("missing command")
	}
	cmd := args[0]
	valid, ok := validFlags[cmd]
	if !ok {
		return usagef("unknown command %q", cmd)
	}
	var op opts
	var sels []string
	for _, a := range args[1:] {
		if len(a) < 2 || a[0] != '-' {
			sels = append(sels, a)
			continue
		}
		p, known := flagPtr(&op, a)
		if !known {
			return usagef("unknown flag %s", a)
		}
		if !slices.Contains(valid, a) {
			return usagef("flag %s is not valid for %s", a, cmd)
		}
		*p = true
	}
	if len(sels) > 0 && !takesSelectors[cmd] {
		return usagef("%s takes no arguments", cmd)
	}

	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprintf(o.stdout, helpText, prog.Name)
		return nil
	case "version":
		if o.json {
			o.writeJSON(map[string]string{"version": Version})
		} else {
			fmt.Fprintf(o.stdout, "%s %s\n", prog.Name, Version)
		}
		return nil
	case "rename":
		if len(sels) != 1 {
			return usagef("rename: give exactly one new name")
		}
		return rename(sels[0], o)
	case "status":
		st, err := api.Status()
		if err != nil {
			return err
		}
		o.status(st)
		return nil
	case "list":
		groups, err := api.List()
		if err != nil {
			return err
		}
		o.list(groups, op.allProcs)
		return nil
	}
	return freezeThaw(cmd == "freeze", sels, op, api, o)
}

// flagPtr maps a flag name to its field in op.
func flagPtr(op *opts, name string) (*bool, bool) {
	switch name {
	case "--json":
		return &op.json, true
	case "--all":
		return &op.all, true
	case "--include-daemons":
		return &op.includeDaemons, true
	case "--yes":
		return &op.yes, true
	case "--force-self":
		return &op.forceSelf, true
	case "--all-procs":
		return &op.allProcs, true
	}
	return nil, false
}

func freezeThaw(freeze bool, sels []string, op opts, api freezer.API, o *output) error {
	verb := "thaw"
	if freeze {
		verb = "freeze"
	}
	if op.all && len(sels) > 0 {
		return usagef("%s: use either --all or selectors, not both", verb)
	}
	if !op.all && len(sels) == 0 {
		return usagef("%s: give --all or at least one selector", verb)
	}
	groups, err := api.List()
	if err != nil {
		return err
	}
	var targets []freezer.GroupView
	if op.all {
		for _, g := range groups {
			if (freeze && !g.Self && (!g.Daemon || op.includeDaemons) && g.State != "frozen") ||
				(!freeze && g.State != "running") {
				targets = append(targets, g)
			}
		}
	} else if targets, err = selectTargets(groups, sels); err != nil {
		return err
	}
	if len(targets) == 0 {
		o.results(freeze, nil)
		return nil
	}
	if freeze && !op.yes {
		var need uint64
		for _, g := range targets {
			need += g.RSS
		}
		st, err := api.Status()
		if err != nil {
			return err
		}
		if need > st.SwapFree {
			return fmt.Errorf("not enough free swap: need %s, have %s; rerun with --yes to freeze anyway (RAM will not be freed)",
				human.Bytes(need), human.Bytes(st.SwapFree))
		}
	}
	results := make([]freezer.Result, 0, len(targets))
	for _, g := range targets {
		var r freezer.Result
		if freeze {
			r = api.Freeze(g, op.forceSelf)
			if g.Status == "busy" {
				r.Warning = joinWarning(r.Warning, "was busy")
			}
		} else {
			r = api.Thaw(g)
		}
		if r.Name == "" {
			r.Name = g.Name
		}
		if r.ID == "" {
			r.ID = g.ID
		}
		if !r.OK {
			o.code = 1
		}
		results = append(results, r)
	}
	o.results(freeze, results)
	return nil
}

// selectTargets resolves each selector to exactly one group and returns the
// targets de-duplicated by Key, in list order.
func selectTargets(groups []freezer.GroupView, sels []string) ([]freezer.GroupView, error) {
	plain := make([]discover.Group, len(groups))
	for i, g := range groups {
		plain[i] = g.Group
	}
	want := map[string]bool{}
	for _, sel := range sels {
		m := discover.Select(plain, sel)
		switch len(m) {
		case 0:
			return nil, usagef("no agent matches %q", sel)
		case 1:
			want[m[0].Key] = true
		default:
			cands := make([]string, len(m))
			for i, g := range m {
				cands[i] = g.ID + " " + g.Name
			}
			return nil, usagef("%q matches several agents: %s", sel, strings.Join(cands, "; "))
		}
	}
	var out []freezer.GroupView
	for _, g := range groups {
		if want[g.Key] {
			out = append(out, g)
			delete(want, g.Key)
		}
	}
	return out, nil
}

func joinWarning(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// freed returns the RAM a freeze result released, clamped at 0.
func freed(r freezer.Result) uint64 {
	if !r.OK || r.RSSAfter >= r.RSSBefore {
		return 0
	}
	return r.RSSBefore - r.RSSAfter
}
