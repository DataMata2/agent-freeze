package human

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1023, "1023B"},
		{12 * 1024, "12K"},
		{12*1024 + 1023, "12K"},
		{340 << 20, "340M"},
		{340<<20 + 1<<19, "340M"},
		{1288490188, "1.2G"},
		{15 << 30, "15.0G"},
		{2048 << 30, "2048.0G"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPath(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	cases := map[string]string{
		"/home/u/x": "~/x",
		"/home/u":   "~",
		"/home/uu":  "/home/uu",
		"/etc":      "/etc",
	}
	for in, want := range cases {
		if got := Path(in); got != want {
			t.Errorf("Path(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathEmptyHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got := Path("/etc"); got != "/etc" {
		t.Errorf("Path = %q", got)
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{45 * time.Second, "45s"},
		{12*time.Minute + 30*time.Second, "12m"},
		{3*time.Hour + 5*time.Minute, "3h05m"},
		{52 * time.Hour, "2d04h"},
	}
	for _, c := range cases {
		if got := Duration(c.in); got != c.want {
			t.Errorf("Duration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
