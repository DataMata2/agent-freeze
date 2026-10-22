//go:build linux

package freezer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/discover"
)

func TestGroupViewJSON(t *testing.T) {
	v := GroupView{Group: discover.Group{ID: "1"}, State: "running"}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":"1"`, `"state":`, `"freed_bytes":`, `"delegated":`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON %s missing %s", s, want)
		}
	}
	if strings.Contains(s, "frozen_at") {
		t.Errorf("JSON %s has frozen_at with nil FrozenAt", s)
	}
}
