package auto

import (
	"strings"
	"testing"
	"time"
)

func TestLaunchdPlistRunsAutoRunAtLoginAndEscapesThePath(t *testing.T) {
	got := launchdPlist("/Users/a&b/bin/chill", "/Users/a&b/.local/state/agent-freeze/auto.err", "/Users/a&b/.local/state")
	for _, want := range []string{
		"<string>" + ServiceLabel + "</string>",
		"<string>/Users/a&amp;b/bin/chill</string>",
		"<string>auto</string>",
		"<string>run</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/Users/a&amp;b/.local/state/agent-freeze/auto.err</string>",
		"<key>XDG_STATE_HOME</key>\n\t\t<string>/Users/a&amp;b/.local/state</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist lacks %q:\n%s", want, got)
		}
	}
}

func TestSystemdUnitRunsAutoRun(t *testing.T) {
	got := systemdUnit("/home/u/my bin/afz", "/home/u/100%")
	for _, want := range []string{`ExecStart="/home/u/my bin/afz" auto run`, `Environment="XDG_STATE_HOME=/home/u/100%%"`,
		"Restart=on-failure", "RestartSec=5", "WantedBy=default.target"} {
		if !strings.Contains(got, want) {
			t.Errorf("unit lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "Environment=") != 1 {
		t.Errorf("unit has extra Environment lines:\n%s", got)
	}
}

func TestSystemdUnitPassesExtraEnvironmentQuoted(t *testing.T) {
	got := systemdUnit("/bin/afz", "/s", "CLAUDE_CONFIG_DIR=/home/u/my $dir/100%", "AFZ_NO_BANNER=1")
	for _, want := range []string{`Environment="CLAUDE_CONFIG_DIR=/home/u/my $dir/100%%"`, `Environment="AFZ_NO_BANNER=1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("unit lacks %q:\n%s", want, got)
		}
	}
}

func TestFocusLineKeepsTerminalsAndUnknownOnly(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"/dev/ttys003":  {"/dev/ttys003", true},
		"  /dev/ttys1 ": {"/dev/ttys1", true},
		"?":             {FocusUnknown, true},
		"execution error: Not authorized to send Apple events to Terminal. (-1743)": {"", false},
		"": {"", false},
	}
	for line, c := range cases {
		got, ok := focusLine(line)
		if got != c.want || ok != c.ok {
			t.Errorf("focusLine(%q) = %q, %v; want %q, %v", line, got, ok, c.want, c.ok)
		}
	}
}

func TestOnlyOneLoopRunsAtATime(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	first, err := LockRun(func() { t.Fatal("the first loop had to wait") })
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	got := make(chan error, 1)
	go func() {
		second, err := LockRun(func() { close(waited) })
		if err == nil {
			second.Close()
		}
		got <- err
	}()
	<-waited
	select {
	case err := <-got:
		t.Fatalf("the second loop started while the first ran: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	first.Close()
	if err := <-got; err != nil {
		t.Fatalf("the second loop did not start after the first stopped: %v", err)
	}
}
