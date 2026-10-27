package freezer

import (
	"strings"

	"github.com/DataMata2/agent-freeze/internal/human"
)

// Match reports whether q is a case-insensitive substring of g's name, cwd
// (full or ~-shortened), agent or ID. An empty q matches every group.
func Match(g GroupView, q string) bool {
	q = strings.ToLower(q)
	for _, s := range []string{g.Name, g.Cwd, human.Path(g.Cwd), g.Agent, g.ID} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}
