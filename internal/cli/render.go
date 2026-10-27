package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/human"
	"github.com/DataMata2/agent-freeze/internal/prog"
)

// maxName is the NAME column width in runes, before any suffix.
const maxName = 40

// output renders results as text or JSON and records the exit code for
// non-error outcomes (1 when any freeze/thaw result failed).
type output struct {
	stdout, stderr io.Writer
	json           bool
	code           int
}

func (o *output) writeJSON(v any) {
	enc := json.NewEncoder(o.stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// fail reports an operation error (exit 1).
func (o *output) fail(err error) {
	if o.json {
		o.writeJSON(map[string]string{"error": err.Error()})
		return
	}
	fmt.Fprintf(o.stderr, "%s: %s\n", prog.Name, err)
}

// truncName shortens s to maxName runes, ending in "…" when cut.
func truncName(s string) string {
	r := []rune(s)
	if len(r) <= maxName {
		return s
	}
	return string(r[:maxName-1]) + "…"
}

// cell replaces control characters (tabs, newlines) so a value cannot break
// the table layout.
func cell(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func displayName(g freezer.GroupView) string {
	n := truncName(cell(g.Name))
	if g.Self {
		n += " (you)"
	}
	if g.Daemon {
		n += " [daemon]"
	}
	return n
}

func (o *output) list(groups []freezer.GroupView, allProcs bool) {
	if o.json {
		if groups == nil {
			groups = []freezer.GroupView{}
		}
		o.writeJSON(struct {
			Groups []freezer.GroupView `json:"groups"`
		}{groups})
		return
	}
	// Lay the table out on its own, then interleave the pids lines: a
	// tab-less line inside tabwriter input would split the column blocks.
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATE\tSTATUS\tRAM\tSWAP\tPROCS\tCWD")
	for _, g := range groups {
		status := g.Status
		if status == "" {
			status = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			g.ID, displayName(g), g.State, status,
			human.Bytes(g.RSS), human.Bytes(g.Swap), len(g.PIDs), cell(human.Path(g.Cwd)))
	}
	_ = tw.Flush()
	lines := strings.SplitAfter(buf.String(), "\n")
	fmt.Fprint(o.stdout, lines[0])
	for i, g := range groups {
		fmt.Fprint(o.stdout, lines[i+1])
		if allProcs {
			pids := make([]string, len(g.PIDs))
			for j, p := range g.PIDs {
				pids[j] = strconv.Itoa(p)
			}
			fmt.Fprintf(o.stdout, "  pids: %s\n", strings.Join(pids, " "))
		}
	}
}

func (o *output) results(freeze bool, results []freezer.Result) {
	var total uint64
	if freeze {
		for _, r := range results {
			total += freed(r)
		}
	}
	if o.json {
		if results == nil {
			results = []freezer.Result{}
		}
		o.writeJSON(struct {
			Results []freezer.Result `json:"results"`
			Freed   uint64           `json:"freed_bytes"`
		}{results, total})
		return
	}
	if len(results) == 0 {
		if freeze {
			fmt.Fprintln(o.stdout, "nothing to freeze")
		} else {
			fmt.Fprintln(o.stdout, "nothing to thaw")
		}
		return
	}
	for _, r := range results {
		var line string
		switch {
		case !r.OK:
			line = fmt.Sprintf("failed %s (%s): %s", r.Name, r.ID, r.Error)
		case freeze:
			line = fmt.Sprintf("froze %s (%s): freed %s", r.Name, r.ID, human.Bytes(freed(r)))
		default:
			line = fmt.Sprintf("thawed %s (%s)", r.Name, r.ID)
		}
		if r.Warning != "" {
			line += " — " + r.Warning
		}
		fmt.Fprintln(o.stdout, line)
	}
	if freeze {
		fmt.Fprintf(o.stdout, "total freed: %s\n", human.Bytes(total))
	}
}

func (o *output) status(st freezer.Status) {
	if o.json {
		o.writeJSON(st)
		return
	}
	fmt.Fprintf(o.stdout, "frozen: %d groups, freed %s, in swap %s\n",
		st.Frozen, human.Bytes(st.Freed), human.Bytes(st.FrozenSwap))
	fmt.Fprintf(o.stdout, "RAM available %s, swap free %s\n",
		human.Bytes(st.MemAvailable), human.Bytes(st.SwapFree))
}
