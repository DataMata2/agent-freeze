package prog

import "testing"

func TestFromArg0(t *testing.T) {
	for _, c := range []struct{ arg0, want string }{
		{"afz", "afz"},
		{"/home/u/go/bin/chill", "chill"},
		{"./nap", "nap"},
		{"", "afz"},
		{"/", "afz"},
	} {
		if got := FromArg0(c.arg0); got != c.want {
			t.Errorf("FromArg0(%q) = %q, want %q", c.arg0, got, c.want)
		}
	}
}
