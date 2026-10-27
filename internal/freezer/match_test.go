package freezer

import (
	"testing"

	"github.com/DataMata2/agent-freeze/internal/discover"
)

func TestMatch(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	g := GroupView{Group: discover.Group{
		ID: "540032", Key: "claude-540032-1000", Agent: "claude",
		Name: "Web Frontend", Cwd: "/home/u/code/acme",
	}}
	cases := []struct {
		q    string
		want bool
	}{
		{"", true},               // empty query matches everything
		{"web", true},            // name, case-insensitive
		{"FRONTEND", true},       // name, upper-case query
		{"acme", true},           // cwd
		{"/home/u/code", true},   // cwd, full path
		{"~/code/ac", true},      // cwd, ~-shortened as shown
		{"CLAUDE", true},         // agent
		{"5400", true},           // id substring
		{"codex", false},         // other agent
		{"~/other", false},       // shortened path that is not there
		{"claude-540032", false}, // key is not searched
		{"zzz", false},
	}
	for _, c := range cases {
		if got := Match(g, c.q); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}
