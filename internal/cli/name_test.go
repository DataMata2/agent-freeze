package cli

import (
	"strings"
	"testing"

	"github.com/DataMata2/agent-freeze/internal/prog"
)

func TestMessagesUseTheCommandName(t *testing.T) {
	defer func(old string) { prog.Name = old }(prog.Name)
	prog.Name = "chill"
	for _, args := range [][]string{{"version"}, {"help"}, {"bogus"}, {"freeze"}} {
		_, out, errs := run(newFake(t), args...)
		all := out + errs
		if !strings.Contains(all, "chill") || strings.Contains(all, "afz") {
			t.Errorf("%v: %q", args, all)
		}
	}
}
