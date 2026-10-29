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
	"github.com/DataMata2/agent-freeze/internal/human"
	"github.com/DataMata2/agent-freeze/internal/pressure"
)

type fakeAPI struct {
	mu        sync.Mutex
	groups    []freezer.GroupView
	lists     int
	froze     []string
	frozeBy   []string
	thawed    []string
	failKey   string
	swapFree  uint64                              // free swap Status reports; 0 means plenty
	statusErr error                               // returned by Status
	listHook  func(n int, gs []freezer.GroupView) // edits what the n-th List (from 1) returns
}

func (f *fakeAPI) List() ([]freezer.GroupView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	gs := append([]freezer.GroupView(nil), f.groups...)
	if f.listHook != nil {
		f.listHook(f.lists, gs)
	}
	return gs, nil
}

func (f *fakeAPI) Status() (freezer.Status, error) {
	free := f.swapFree
	if free == 0 {
		free = 1 << 50
	}
	return freezer.Status{SwapFree: free}, f.statusErr
}

// FreezeAs records the freeze and, unless g is failKey, marks g frozen by by.
func (f *fakeAPI) FreezeAs(g freezer.GroupView, forceSelf bool, by string) freezer.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if forceSelf {
		panic("auto mode must never force-freeze its own group")
	}
	f.froze = append(f.froze, g.Name)
	f.frozeBy = append(f.frozeBy, by)
	if g.Key == f.failKey {
		return freezer.Result{Name: g.Name, Error: "boom"}
	}
	for i := range f.groups {
		if f.groups[i].Key == g.Key {
			f.groups[i].State, f.groups[i].FrozenBy = "frozen", by
		}
	}
	return freezer.Result{Name: g.Name, OK: true}
}

func (f *fakeAPI) Thaw(g freezer.GroupView) freezer.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thawed = append(f.thawed, g.Name)
	for i := range f.groups {
		if f.groups[i].Key == g.Key {
			f.groups[i].State, f.groups[i].FrozenBy = "running", ""
		}
	}
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

		PendingInput: func(string) int { return 0 },
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
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	sibling := idleGroup("sibling", time.Hour, gib)
	sibling.State, sibling.TTY, sibling.FrozenBy = "partial", "/dev/frozen", "auto"
	other := idleGroup("other", time.Hour, gib)
	other.State, other.FrozenBy = "frozen", "auto"
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
	go func() { done <- run.Run(ctx, path, tabs, InputWatch{}) }()
	return tabs, func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
}

func TestRunThawsOnFocusAndStopsWithTheContext(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State, frozen.FrozenBy = "frozen", "auto"
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
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	api := &fakeAPI{groups: []freezer.GroupView{frozen}}
	cfg := autoCfg()
	cfg.TabThaw = false
	if err := newRunner(api, calm, &recorder{}).ThawFocused(cfg, "/dev/frozen"); err != nil || len(api.thawed) != 0 || api.lists != 0 {
		t.Fatalf("err=%v thawed=%v lists=%d", err, api.thawed, api.lists)
	}
}

func TestCheckFreezesAsAuto(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
	if err := newRunner(api, memWarn, &recorder{}).Check(autoCfg(), noFocus()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.frozeBy, []string{"auto"}) {
		t.Fatalf("froze by %q, want [auto]", api.frozeBy)
	}
}

func TestCheckSkipsAGroupWithoutEnoughSwapAndBacksOff(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("big", time.Hour, gib)}, swapFree: 512 << 20}
	rec := &recorder{}
	run := newRunner(api, memWarn, rec)
	clock := now
	run.Now = func() time.Time { return clock }
	for range 3 {
		if err := run.Check(autoCfg(), noFocus()); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(30 * time.Second)
	}
	if len(api.froze) != 0 {
		t.Fatalf("froze %v without enough free swap", api.froze)
	}
	want := "not enough free swap: need " + human.Bytes(gib) + ", have " + human.Bytes(512<<20)
	if len(rec.events) != 1 || rec.events[0].Action != "skipped" || rec.events[0].Reason != want {
		t.Fatalf("events %+v, want one skipped event %q", rec.events, want)
	}
	clock = now.Add(31 * time.Minute)
	if err := run.Check(autoCfg(), noFocus()); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 2 || rec.events[1].Action != "skipped" {
		t.Fatalf("events %+v, want a second skipped event after the retry wait", rec.events)
	}
}

func TestCheckReturnsStatusErrorsWithoutFreezing(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}, statusErr: errors.New("no meminfo")}
	if err := newRunner(api, memWarn, &recorder{}).Check(autoCfg(), noFocus()); err == nil || len(api.froze) != 0 {
		t.Fatalf("err=%v froze=%v, want the status error and no freeze", err, api.froze)
	}
}

func TestCheckWaitsOutACooldownAfterAFreeze(t *testing.T) {
	for _, tc := range []struct {
		interval float64
		want     int
	}{
		{30, 3}, // cooldown 2*30s = 60s: freezes at 0, 60 and 120s
		{5, 3},  // cooldown at least 60s
		{60, 2}, // cooldown 2*60s = 120s: freezes at 0 and 120s
	} {
		var groups []freezer.GroupView
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			groups = append(groups, idleGroup(n, time.Hour, gib))
		}
		api := &fakeAPI{groups: groups}
		rec := &recorder{}
		run := newRunner(api, memWarn, rec)
		clock := now
		run.Now = func() time.Time { return clock }
		cfg := autoCfg()
		cfg.IntervalSeconds = tc.interval
		for range 5 {
			if err := run.Check(cfg, noFocus()); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(30 * time.Second)
		}
		if len(api.froze) != tc.want || len(rec.events) != tc.want {
			t.Errorf("interval %vs: froze %v, events %d, want %d freezes over 5 checks 30s apart",
				tc.interval, api.froze, len(rec.events), tc.want)
		}
	}
}

func TestCheckAfterTheCooldownFreezesOnlyIfPressureStillHolds(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("a", time.Hour, gib), idleGroup("b", time.Hour, gib)}}
	run := newRunner(api, memWarn, &recorder{})
	clock := now
	run.Now = func() time.Time { return clock }
	reading := memWarn
	run.Read = func() (pressure.Reading, error) { return reading, nil }
	if err := run.Check(autoCfg(), noFocus()); err != nil || len(api.froze) != 1 {
		t.Fatalf("err=%v froze=%v, want one freeze", err, api.froze)
	}
	reading = calm
	clock = clock.Add(time.Minute)
	if err := run.Check(autoCfg(), noFocus()); err != nil || len(api.froze) != 1 {
		t.Fatalf("err=%v froze=%v, want no second freeze once pressure is gone", err, api.froze)
	}
}

func TestCheckRechecksTheGroupRightBeforeFreezing(t *testing.T) {
	for name, change := range map[string]func(g *freezer.GroupView){
		"busy":    func(g *freezer.GroupView) { g.Status = "busy" },
		"frozen":  func(g *freezer.GroupView) { g.State = "frozen" },
		"gone":    func(g *freezer.GroupView) { g.Key = "other-key" },
		"waiting": func(g *freezer.GroupView) { g.Status = "waiting" },
	} {
		api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
		api.listHook = func(n int, gs []freezer.GroupView) {
			if n > 1 {
				change(&gs[0])
			}
		}
		rec := &recorder{}
		if err := newRunner(api, memWarn, rec).Check(autoCfg(), noFocus()); err != nil {
			t.Fatal(err)
		}
		if len(api.froze) != 0 || len(rec.events) != 0 {
			t.Errorf("%s by the re-check: froze %v, events %+v, want nothing", name, api.froze, rec.events)
		}
	}
}

func TestThawFocusedLeavesAManuallyFrozenGroupFrozen(t *testing.T) {
	manual := idleGroup("manual", time.Hour, gib)
	manual.State = "frozen"
	api := &fakeAPI{groups: []freezer.GroupView{manual}}
	rec := &recorder{}
	if err := newRunner(api, calm, rec).ThawFocused(autoCfg(), "/dev/manual"); err != nil {
		t.Fatal(err)
	}
	if len(api.thawed) != 0 || len(rec.events) != 0 {
		t.Fatalf("thawed %v, events %+v, want a manual freeze left alone", api.thawed, rec.events)
	}
}

func TestCheckInAFreshRunnerWaitsOutTheCooldownOfAnEarlierAutoFreeze(t *testing.T) {
	for _, tc := range []struct {
		ago  time.Duration
		by   string
		want int
	}{
		{10 * time.Second, "auto", 0}, // a restarted service must not freeze again at once
		{5 * time.Minute, "auto", 1},
		{10 * time.Second, "", 1}, // a manual freeze starts no cooldown
	} {
		at := now.Add(-tc.ago)
		earlier := idleGroup("earlier", time.Hour, gib)
		earlier.State, earlier.FrozenBy, earlier.FrozenAt = "frozen", tc.by, &at
		api := &fakeAPI{groups: []freezer.GroupView{earlier, idleGroup("next", time.Hour, gib)}}
		rec := &recorder{}
		cfg := autoCfg()
		cfg.IntervalSeconds = 30
		if err := newRunner(api, memWarn, rec).Check(cfg, noFocus()); err != nil {
			t.Fatal(err)
		}
		if len(api.froze) != tc.want || len(rec.events) != tc.want {
			t.Errorf("frozen by %q %v ago: froze %v, events %+v, want %d freezes", tc.by, tc.ago, api.froze, rec.events, tc.want)
		}
	}
}

func TestCheckNotifiesASkippedFreeze(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("big", time.Hour, gib)}, swapFree: 512 << 20}
	rec := &recorder{}
	run := newRunner(api, memWarn, rec)
	clock := now
	run.Now = func() time.Time { return clock }
	for range 3 {
		if err := run.Check(autoCfg(), noFocus()); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(30 * time.Second)
	}
	if len(rec.notices) != 1 || !strings.Contains(rec.notices[0], "not enough free swap") {
		t.Fatalf("notices %q, want one about free swap", rec.notices)
	}
	cfg := autoCfg()
	cfg.Notify = false
	quiet := &recorder{}
	if err := newRunner(api, memWarn, quiet).Check(cfg, noFocus()); err != nil || len(quiet.notices) != 0 {
		t.Fatalf("err=%v notices %q with notifications off", err, quiet.notices)
	}
}

func TestCheckSkipsAGroupWithPendingInput(t *testing.T) {
	api := &fakeAPI{groups: []freezer.GroupView{idleGroup("old", time.Hour, gib)}}
	rec := &recorder{}
	run := newRunner(api, memWarn, rec)
	var asked []string
	run.PendingInput = func(tty string) int { asked = append(asked, tty); return 1 }
	if err := run.Check(autoCfg(), noFocus()); err != nil {
		t.Fatal(err)
	}
	if len(api.froze) != 0 || len(rec.events) != 0 || len(rec.notices) != 0 {
		t.Fatalf("froze %v, events %+v, notices %v; want nothing while input is pending", api.froze, rec.events, rec.notices)
	}
	if !slices.Equal(asked, []string{"/dev/old"}) {
		t.Fatalf("asked %v, want the group's tty", asked)
	}
	// The group is backed off, so a tty that keeps unread bytes is not picked at every check.
	run.PendingInput = func(string) int { return 0 }
	if err := run.Check(autoCfg(), noFocus()); err != nil || len(api.froze) != 0 {
		t.Fatalf("err=%v froze=%v, want the group backed off after the skip", err, api.froze)
	}
	run.Now = func() time.Time { return now.Add(31 * time.Minute) }
	if err := run.Check(autoCfg(), noFocus()); err != nil || !slices.Equal(api.froze, []string{"old"}) {
		t.Fatalf("err=%v froze=%v, want the freeze after the retry wait", err, api.froze)
	}
	if len(rec.notices) != 1 {
		t.Fatalf("notices %v, want only the freeze notice", rec.notices)
	}
}

func TestThawInputThawsOnlyAutoFreezesOnThatTTY(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	manual := idleGroup("manual", time.Hour, gib)
	manual.State, manual.TTY = "frozen", frozen.TTY
	other := idleGroup("other", time.Hour, gib)
	other.State, other.FrozenBy = "partial", "auto"
	api := &fakeAPI{groups: []freezer.GroupView{frozen, manual, other}}
	rec := &recorder{}
	if err := newRunner(api, calm, rec).ThawInput(autoCfg(), "/dev/frozen"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.thawed, []string{"frozen"}) || len(rec.events) != 1 ||
		rec.events[0].Action != "thawed" || rec.events[0].Reason != "input" {
		t.Fatalf("thawed %v, events %+v", api.thawed, rec.events)
	}
	off := autoCfg()
	off.InputThaw = false
	for _, cfg := range []Config{off, Defaults()} {
		if err := newRunner(api, calm, rec).ThawInput(cfg, "/dev/other"); err != nil || len(api.thawed) != 1 {
			t.Errorf("ThawInput with %+v thawed %v", cfg, api.thawed)
		}
	}
}

// fakeInput is the Run side of an input watcher: what Run offered, and events to send it.
func fakeInput() (InputWatch, chan string) {
	ev := make(chan string)
	return InputWatch{TTYs: make(chan []string, 1), Events: ev}, ev
}

func nextSet(t *testing.T, in InputWatch) []string {
	t.Helper()
	select {
	case set := <-in.TTYs:
		return set
	case <-time.After(5 * time.Second):
		t.Fatal("Run offered no tty set")
		return nil
	}
}

func TestRunOffersAutoFrozenTTYsAndThawsOnInput(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	sibling := idleGroup("sibling", time.Hour, gib)
	sibling.State, sibling.FrozenBy, sibling.TTY = "partial", "auto", frozen.TTY
	other := idleGroup("other", time.Hour, gib)
	other.State, other.FrozenBy = "frozen", "auto"
	manual := idleGroup("manual", time.Hour, gib)
	manual.State = "frozen"
	running := idleGroup("running", time.Hour, gib)
	notty := idleGroup("notty", time.Hour, gib)
	notty.State, notty.FrozenBy, notty.TTY = "frozen", "auto", ""
	api := &fakeAPI{groups: []freezer.GroupView{frozen, sibling, other, manual, running, notty}}
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := SaveConfig(path, autoCfg()); err != nil {
		t.Fatal(err)
	}
	run := newRunner(api, calm, &recorder{})
	var mu sync.Mutex
	var events []Event
	run.Log = func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() }
	in, ev := fakeInput()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run.Run(ctx, path, nil, in) }()

	if set := nextSet(t, in); !slices.Equal(set, []string{"/dev/frozen", "/dev/other"}) {
		t.Fatalf("after a check Run offered %v", set)
	}
	ev <- "/dev/frozen"
	// The thawed tty leaves the set: a failed thaw is not retried until the next check.
	if set := nextSet(t, in); !slices.Equal(set, []string{"/dev/other"}) {
		t.Fatalf("after a thaw Run offered %v", set)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := api.thawedNames(); !slices.Equal(got, []string{"frozen", "sibling"}) {
		t.Fatalf("thawed %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Reason != "input" {
		t.Fatalf("events %+v", events)
	}
}

func TestRunOffersNothingWithInputThawOff(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	api := &fakeAPI{groups: []freezer.GroupView{frozen}}
	path := filepath.Join(t.TempDir(), "auto.json")
	cfg := autoCfg()
	cfg.InputThaw = false
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	in, ev := fakeInput()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newRunner(api, calm, &recorder{}).Run(ctx, path, nil, in) }()
	if set := nextSet(t, in); len(set) != 0 {
		t.Fatalf("Run offered %v with input-thaw off", set)
	}
	ev <- "/dev/frozen"
	nextSet(t, in)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := api.thawedNames(); len(got) != 0 {
		t.Fatalf("thawed %v with input-thaw off", got)
	}
}

// fastChecks makes Run check every few milliseconds.
func fastChecks(t *testing.T) {
	t.Helper()
	old := checkEvery
	checkEvery = func(Config) time.Duration { return 5 * time.Millisecond }
	t.Cleanup(func() { checkEvery = old })
}

// syncClock is a test clock that Run's goroutine can read.
type syncClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *syncClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *syncClock) add(d time.Duration) { c.mu.Lock(); c.at = c.at.Add(d); c.mu.Unlock() }

func TestRunDoesNotRefreezeASessionThawedByInput(t *testing.T) {
	fastChecks(t)
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	api := &fakeAPI{groups: []freezer.GroupView{frozen}}
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := SaveConfig(path, autoCfg()); err != nil {
		t.Fatal(err)
	}
	clock := &syncClock{at: now}
	run := newRunner(api, memWarn, &recorder{})
	run.Now, run.Log, run.Notify = clock.now, func(Event) {}, func(string, string) {}
	in, ev := fakeInput()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run.Run(ctx, path, nil, in) }()
	nextSet(t, in)
	ev <- "/dev/frozen"
	// Past the cooldown, short of the idle time: the thaw counts as use.
	clock.add(3 * time.Minute)
	for range 5 {
		nextSet(t, in)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.thawed) != 1 || len(api.froze) != 0 {
		t.Fatalf("thawed %v, froze %v; want one thaw and no refreeze", api.thawed, api.froze)
	}
}

func TestRunOffersTheTTYSetEvenWhenTheCheckFails(t *testing.T) {
	frozen := idleGroup("frozen", time.Hour, gib)
	frozen.State, frozen.FrozenBy = "frozen", "auto"
	api := &fakeAPI{groups: []freezer.GroupView{frozen}}
	path := filepath.Join(t.TempDir(), "auto.json")
	if err := SaveConfig(path, autoCfg()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	old := stderr
	stderr = &out
	t.Cleanup(func() { stderr = old })
	run := newRunner(api, calm, &recorder{})
	run.Read = func() (pressure.Reading, error) { return pressure.Reading{}, errors.New("no pressure file") }
	in, _ := fakeInput()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run.Run(ctx, path, nil, in) }()
	set := nextSet(t, in)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(set, []string{"/dev/frozen"}) {
		t.Fatalf("offered %v after a failed check", set)
	}
	if !strings.Contains(out.String(), "no pressure file") {
		t.Fatalf("the check error was not reported: %q", out.String())
	}
}
