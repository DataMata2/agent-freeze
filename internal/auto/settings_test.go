package auto

import (
	"reflect"
	"slices"
	"testing"
)

func setting(t *testing.T, name string) Setting {
	t.Helper()
	s, ok := Lookup(name)
	if !ok {
		t.Fatalf("no setting %q", name)
	}
	return s
}

func TestEveryKnobIsASetting(t *testing.T) {
	var names []string
	for _, s := range Settings {
		names = append(names, s.Name)
		if s.Label == "" || s.Help == "" {
			t.Errorf("%s has no label or help", s.Name)
		}
	}
	want := []string{"idle", "interval", "memory", "cpu-load", "cpu-use", "away", "retry", "notify", "tab-thaw", "input-thaw"}
	if !slices.Equal(names, want) {
		t.Fatalf("settings = %v, want %v", names, want)
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup found a setting that does not exist")
	}
}

func TestDefaultValues(t *testing.T) {
	want := map[string]string{
		"idle": "15", "interval": "30", "memory": "warn", "cpu-load": "1", "cpu-use": "10",
		"away": "5", "retry": "30", "notify": "on", "tab-thaw": "on", "input-thaw": "on",
	}
	for name, v := range want {
		if got := setting(t, name).Value(Defaults()); got != v {
			t.Errorf("%s = %q, want %q", name, got, v)
		}
	}
}

func TestSetReturnsAChangedCopy(t *testing.T) {
	c := Defaults()
	cases := map[string]string{"idle": "0", "interval": "120", "memory": "critical", "cpu-load": "0",
		"cpu-use": "50", "away": "2.5", "retry": "60", "notify": "off", "tab-thaw": "off", "input-thaw": "off"}
	for name, v := range cases {
		got, err := setting(t, name).Set(c, v)
		if err != nil {
			t.Fatalf("Set %s %s: %v", name, v, err)
		}
		if setting(t, name).Value(got) != v {
			t.Errorf("after Set %s %s it reads %q", name, v, setting(t, name).Value(got))
		}
	}
	if !reflect.DeepEqual(c, Defaults()) {
		t.Errorf("Set changed the original: %+v", c)
	}
}

func TestSetRejectsBadValues(t *testing.T) {
	for _, tc := range [][2]string{
		{"idle", "-1"}, {"idle", "soon"}, {"idle", "NaN"}, {"interval", "2"}, {"interval", "1e9"},
		{"memory", "high"}, {"notify", "maybe"}, {"cpu-load", "Inf"},
	} {
		if _, err := setting(t, tc[0]).Set(Defaults(), tc[1]); err == nil {
			t.Errorf("Set %s %q accepted", tc[0], tc[1])
		}
	}
}

func TestStepMovesThroughValuesAndStopsAtTheEnds(t *testing.T) {
	c := Defaults()
	step := func(name string, dir int) string {
		c = setting(t, name).Step(c, dir)
		return setting(t, name).Value(c)
	}
	if got := step("idle", 1); got != "20" {
		t.Errorf("idle up = %s", got)
	}
	for range 10 {
		step("idle", -1)
	}
	if got := setting(t, "idle").Value(c); got != "0" {
		t.Errorf("idle stepped below 0: %s", got)
	}
	if got := step("cpu-load", 1); got != "1.25" {
		t.Errorf("cpu-load up = %s", got)
	}
	if a, b, d := step("memory", 1), step("memory", 1), step("memory", 1); a != "critical" || b != "off" || d != "warn" {
		t.Errorf("memory cycle = %s %s %s", a, b, d)
	}
	if got := step("notify", 1); got != "off" {
		t.Errorf("notify toggle = %s", got)
	}
}
