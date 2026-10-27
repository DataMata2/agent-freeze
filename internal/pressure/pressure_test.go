package pressure

import (
	"encoding/binary"
	"testing"
)

func TestParsePSI(t *testing.T) {
	cases := []struct {
		text string
		want Level
	}{
		{"some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", Normal},
		{"some avg10=12.50 avg60=3.00 avg300=1.00 total=9\nfull avg10=2.00 avg60=0.00 avg300=0.00 total=1\n", Warn},
		{"some avg10=40.00 avg60=9.00 avg300=1.00 total=9\nfull avg10=15.00 avg60=1.00 avg300=0.00 total=1\n", Critical},
	}
	for _, c := range cases {
		got, err := parsePSI(c.text)
		if err != nil || got != c.want {
			t.Errorf("parsePSI(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
	if _, err := parsePSI("garbage"); err == nil {
		t.Error("parsePSI(garbage) err = nil, want error")
	}
}

func TestParseLoadavg(t *testing.T) {
	got, err := parseLoadavg("3.52 2.10 1.00 2/411 9876\n")
	if err != nil || got != 3.52 {
		t.Fatalf("parseLoadavg = %v, %v; want 3.52", got, err)
	}
	if _, err := parseLoadavg(""); err == nil {
		t.Error("parseLoadavg(\"\") err = nil, want error")
	}
}

func TestDarwinLevel(t *testing.T) {
	for v, want := range map[uint32]Level{1: Normal, 2: Warn, 4: Critical, 0: Normal} {
		if got := darwinLevel(v); got != want {
			t.Errorf("darwinLevel(%d) = %v, want %v", v, got, want)
		}
	}
}

func TestParseDarwinLoadavg(t *testing.T) {
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }: three uint32, 4 bytes padding, int64.
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[0:], 4096) // 2.0 at fscale 2048
	binary.LittleEndian.PutUint64(raw[16:], 2048)
	got, err := parseDarwinLoadavg(raw)
	if err != nil || got != 2.0 {
		t.Fatalf("parseDarwinLoadavg = %v, %v; want 2.0", got, err)
	}
	if _, err := parseDarwinLoadavg(raw[:8]); err == nil {
		t.Error("parseDarwinLoadavg(short) err = nil, want error")
	}
}

func TestLevelString(t *testing.T) {
	if Normal.String() != "normal" || Warn.String() != "warn" || Critical.String() != "critical" {
		t.Errorf("Level strings = %q %q %q", Normal, Warn, Critical)
	}
}

func TestReadOnThisMachine(t *testing.T) {
	r, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	t.Logf("memory %s, load per CPU %.2f", r.Memory, r.LoadPerCPU)
	if r.LoadPerCPU < 0 {
		t.Errorf("LoadPerCPU = %v, want >= 0", r.LoadPerCPU)
	}
}
