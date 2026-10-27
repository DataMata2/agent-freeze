package pressure

import (
	"os"
	"reflect"
	"testing"
)

func TestParsePS(t *testing.T) {
	got := parsePS("    1   0.0\n  123  12.5\n 4567 101.3\nbad line\n")
	want := map[int]float64{1: 0, 123: 12.5, 4567: 101.3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePS = %v, want %v", got, want)
	}
}

func TestParseStatTicks(t *testing.T) {
	// comm may hold spaces and parens; utime and stime are fields 14 and 15.
	stat := "42 (node (worker) x) S 1 42 42 0 -1 4194304 100 0 0 0 250 50 0 0 20 0 9 0 12345 0 0\n"
	got, err := parseStatTicks(stat)
	if err != nil || got != 300 {
		t.Fatalf("parseStatTicks = %v, %v; want 300", got, err)
	}
	if _, err := parseStatTicks("42 (x) S 1"); err == nil {
		t.Error("parseStatTicks(short) err = nil, want error")
	}
}

func TestGroupCPUSumsTheGroupsPIDs(t *testing.T) {
	perPID := map[int]float64{10: 1.5, 11: 20, 12: 0.5, 99: 70}
	if got := GroupCPU(perPID, []int{10, 11, 12, 13}); got != 22 {
		t.Fatalf("GroupCPU = %v, want 22", got)
	}
}

func TestSamplerSeesThisProcess(t *testing.T) {
	var s Sampler
	s.Sample()
	got := s.Sample()
	if _, ok := got[os.Getpid()]; !ok {
		t.Fatalf("Sample has no entry for this process (%d)", os.Getpid())
	}
}
