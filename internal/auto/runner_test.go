package auto

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/pressure"
)

type fakeAPI struct {
	mu      sync.Mutex
	groups  []freezer.GroupView
	lists   int
	froze   []string
	thawed  []string
	failKey string
}

func (f *fakeAPI) List() ([]freezer.GroupView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return append([]freezer.GroupView(nil), f.groups...), nil
}

func (f *fakeAPI) Status() (freezer.Status, error) { return freezer.Status{}, nil }

func (f *fakeAPI) Freeze(g freezer.GroupView, forceSelf bool) freezer.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if forceSelf {
		panic("auto mode must never force-freeze its own group")
	}
	f.froze = append(f.froze, g.Name)
	if g.Key == f.failKey {
		return freezer.Result{Name: g.Name, Error: "boom"}
	}
	return freezer.Result{Name: g.Name, OK: true}
}

func (f *fakeAPI) Thaw(g freezer.GroupView) freezer.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thawed = append(f.thawed, g.Name)
	return freezer.Result{Name: g.Name, OK: true}
}

func (f *fakeAPI) thawedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.thawed...)
}

type recorder struct {
	events  []Event
	notices []string
}

func newRunner(api *fakeAPI, r pressure.Reading, rec *recorder) *Runner {
	return &Runner{
		API:    api,
		Read:   func() (pressure.Reading, error) { return r, nil },
		CPU:    func() map[int]float64 { return map[int]float64{} },
		Notify: func(title, body string) { rec.notices = append(rec.notices, body) },
		Now:    func() time.Time { return now },
		Log:    func(e Event) { rec.events = append(rec.events, e) },
	}
}

func autoCfg() Config {
	c := Defaults()
	c.Mode = "auto"
	return c
}

func noFocus() Focus { return Focus{TTY: ""} }

func TestCheckDoesNothingInManualMode(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
	rec := &recorder{}
	if err := newRunner(api, memWarn, rec).Check(Defaults(), noFocus()); err != nil || api.lists != 0 || len(rec.events) != 0 {
		t.Fatalf("Check in manual mode: err=%v lists=%d events=%v", err, api.lists, rec.events)
	}
}

func TestCheckSkipsTheProcessListWhenCalm(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
	rec := &recorder{}
	if err := newRunner(api, calm, rec).Check(autoCfg(), noFocus()); err != nil || api.lists != 0 || len(rec.events) != 0 {
		t.Fatalf("Check when calm: err=%v lists=%d events=%v, want no work", err, api.lists, rec.events)
	}
}

func TestCheckSamplesCPUEveryTimeSoLinuxMeasuresOneInterval(t *testing.T) {
	run := newRunner(&fakeAPI{}, calm, &recorder{})
	samples := 0
	run.CPU = func() map[int]float64 { samples++; return nil }
	for range 2 {
		if err := run.Check(autoCfg(), noFocus()); err != nil {
			t.Fatal(err)
		}
	}
	if samples != 2 {
		t.Fatalf("CPU sampled %d times in 2 calm checks, want 2", samples)
	}
}

func TestCheckFreezesOneGroupLogsAndNotifies(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{
		idleGroup("old", 3*time.Hour, gib), idleGroup("older", 5*time.Hour, gib),
	}}
	rec := &recorder{}
	if err := newRunner(api, memWarn, rec).Check(autoCfg(), noFocus()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(api.froze) != 1 || api.froze[0] != "older" {
		t.Fatalf("froze %v, want [older]", api.froze)
	}
	if len(rec.events) != 1 || len(rec.notices) != 1 {
		t.Fatalf("events %v, notices %v", rec.events, rec.notices)
	}
	ev := rec.events[0]
	if ev.Action != "froze" || ev.Name != "older" || !strings.Contains(rec.notices[0], "older") || !strings.Contains(ev.String(), "memory pressure") {
		t.Errorf("event %+v / notice %q", ev, rec.notices[0])
	}
}

func TestCheckLogsAFailedFreezeWithoutNotifying(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}, failKey: "old-key"}
	rec := &recorder{}
	if err := newRunner(api, memWarn, rec).Check(autoCfg(), noFocus()); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Action != "failed" || rec.events[0].Error != "boom" || len(rec.notices) != 0 {
		t.Fatalf("events %+v, notices %v", rec.events, rec.notices)
	}
}

func TestCheckMovesOnFromAGroupThatFailedAndRetriesItLater(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{
		idleGroup("stuck", 5*time.Hour, gib), idleGroup("next", time.Hour, gib),
	}, failKey: "stuck-key"}
	run := newRunner(api, memWarn, &recorder{})
	clock := now
	run.Now = func() time.Time { return clock }
	for range 2 {
		if err := run.Check(autoCfg(), noFocus()); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"stuck", "next"}; !slices.Equal(api.froze, want) {
		t.Fatalf("froze %v, want %v", api.froze, want)
	}
	clock = clock.Add(31 * time.Minute)
	if err := run.Check(autoCfg(), noFocus()); err != nil {
		t.Fatal(err)
	}
	if api.froze[len(api.froze)-1] != "stuck" {
		t.Fatalf("froze %v, want a retry of stuck after 30 minutes", api.froze)
	}
}

func TestCheckUsesGroupCPUWhenTheCPUIsBusy(t *testing.T) {
	spinning := idleGroup("spinning", time.Hour, gib)
	spinning.PIDs = []int{50, 51}
	quiet := idleGroup("quiet", 5*time.Hour, gib)
	quiet.PIDs = []int{60}
	api := &fakeAPI{groups: []freezer.GroupView{quiet, spinning}}
	run := newRunner(api, cpuBusy, &recorder{})
	run.CPU = func() map[int]float64 { return map[int]float64{50: 30, 51: 25, 60: 1} }
	if err := run.Check(autoCfg(), noFocus()); err != nil || len(api.froze) != 1 || api.froze[0] != "spinning" {
		t.Fatalf("froze %v (err %v), want [spinning]", api.froze, err)
	}
}

func TestCheckReturnsReadErrors(t *testing.T) {
	run := newRunner(&fakeAPI{}, calm, &recorder{})
	run.Read = func() (pressure.Reading, error) { return pressure.Reading{}, errors.New("no sysctl") }
	if err := run.Check(autoCfg(), noFocus()); err == nil {
		t.Fatal("Check err = nil, want the read error")
	}
}

func TestThawFocusedThawsEveryFrozenGroupOnThatTerminal(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State = "frozen"
	sibling := idleGroup("sibling", time.Hour, gib)
	sibling.State, sibling.TTY = "partial", "/dev/frozen"
	other := idleGroup("other", time.Hour, gib)
	other.State = "frozen"
	api := &fakeAPI{groups: []freezer.GroupView{other, frozen, sibling}}
	rec := &recorder{}
	if err := newRunner(api, calm, rec).ThawFocused(autoCfg(), "/dev/frozen"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"frozen", "sibling"}; !slices.Equal(api.thawed, want) || len(rec.events) != 2 || rec.events[0].Action != "thawed" {
		t.Fatalf("thawed %v, events %+v", api.thawed, rec.events)
	}
	for _, tty := range []string{"", FocusUnknown, "/dev/nobody"} {
		if err := newRunner(api, calm, rec).ThawFocused(autoCfg(), tty); err != nil || len(api.thawed) != 2 {
			t.Errorf("ThawFocused(%q) thawed %v", tty, api.thawed)
		}
	}
	if err := newRunner(api, calm, rec).ThawFocused(Defaults(), "/dev/other"); err != nil || len(api.thawed) != 2 {
		t.Errorf("ThawFocused in manual mode thawed %v", api.thawed)
	}
}

func runInBackground(t *testing.T, run *Runner, path string) (chan<- string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tabs := make(chan string)
	done := make(chan error, 1)
	go func() { done <- run.Run(ctx, path, tabs) }()
	return tabs, func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
}

func TestRunThawsOnFocusAndStopsWithTheContext(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State = "frozen"
	api := &fakeAPI{groups: []freezer.GroupView{frozen}}
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := SaveConfig(path, autoCfg()); err != nil {
		t.Fatal(err)
	}
	run := newRunner(api, calm, &recorder{})
	run.Log = func(Event) {}
	tabs, stop := runInBackground(t, run, path)
	tabs <- "/dev/frozen"
	tabs <- "/dev/frozen"
	stop()
	if got := api.thawedNames(); len(got) == 0 || got[0] != "frozen" {
		t.Fatalf("thawed %v, want [frozen]", got)
	}
}

func TestRunPrintsARepeatedErrorOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	old := stderr
	stderr = &out
	t.Cleanup(func() { stderr = old })
	tabs, stop := runInBackground(t, newRunner(&fakeAPI{}, calm, &recorder{}), path)
	for _, tty := range []string{"/dev/a", "/dev/b", "/dev/c"} {
		tabs <- tty
	}
	stop()
	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Fatalf("printed %d lines, want 1:\n%s", n, out.String())
	}
}

func TestCheckDoesNotListWhenTheTriggersAreOff(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
	cfg := autoCfg()
	cfg.MemoryLevel, cfg.CPULoad = "off", 0
	if err := newRunner(api, memCrit, &recorder{}).Check(cfg, noFocus()); err != nil || api.lists != 0 {
		t.Fatalf("err=%v lists=%d, want no work with both triggers off", err, api.lists)
	}
}

func TestCheckWithNotificationsOffStaysQuiet(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
	rec := &recorder{}
	cfg := autoCfg()
	cfg.Notify = false
	if err := newRunner(api, memWarn, rec).Check(cfg, noFocus()); err != nil || len(api.froze) != 1 || len(rec.notices) != 0 {
		t.Fatalf("err=%v froze=%v notices=%v", err, api.froze, rec.notices)
	}
}

func TestCheckTakesTheRetryWaitFromSettings(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("stuck", time.Hour, gib)}, failKey: "stuck-key"}
	run := newRunner(api, memWarn, &recorder{})
	cfg := autoCfg()
	cfg.RetryMinutes = 0
	for range 2 {
		if err := run.Check(cfg, noFocus()); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.froze) != 2 {
		t.Fatalf("froze %v, want two tries with no retry wait", api.froze)
	}
}

func TestThawFocusedDoesNothingWithTabThawOff(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State = "frozen"
	api := &fakeAPI{groups: []freezer.GroupView{frozen}}
	cfg := autoCfg()
	cfg.TabThaw = false
	if err := newRunner(api, calm, &recorder{}).ThawFocused(cfg, "/dev/frozen"); err != nil || len(api.thawed) != 0 || api.lists != 0 {
		t.Fatalf("err=%v thawed=%v lists=%d", err, api.thawed, api.lists)
	}
}
