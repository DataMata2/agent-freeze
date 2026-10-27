package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataMata2/agent-freeze/internal/auto"
	"github.com/DataMata2/agent-freeze/internal/freezer"
	"github.com/DataMata2/agent-freeze/internal/pressure"
	"github.com/DataMata2/agent-freeze/internal/prog"
)

const serviceStartWait = 3 * time.Second

// Seams for tests: the background service.
var (
	installService   = auto.Install
	uninstallService = auto.Uninstall
	serviceRunning   = auto.Running
)

func autoCmd(args []string, api freezer.API, o *output) error {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if !slices.Contains([]string{"status", "on", "off", "set", "run"}, sub) {
		return usagef("auto: unknown subcommand %q (status, on, off, set, run)", sub)
	}
	if sub == "set" && len(args) != 2 {
		return usagef("auto set: give a setting and a value, e.g. 'auto set idle 0' (%s)", settingNames())
	}
	if sub != "set" && len(args) > 0 {
		return usagef("auto %s takes no arguments", sub)
	}
	switch sub {
	case "on", "off":
		if err := SetAuto(sub == "on"); err != nil {
			return err
		}
		if sub == "on" {
			waitForService()
		}
	case "set":
		setting, ok := auto.Lookup(args[0])
		if !ok {
			return usagef("auto set: unknown setting %q (%s)", args[0], settingNames())
		}
		cfg, err := auto.LoadConfig(auto.DefaultPath())
		if err != nil {
			return err
		}
		if cfg, err = setting.Set(cfg, args[1]); err != nil {
			return usagef("auto set %s", err)
		}
		if err := auto.SaveConfig(auto.DefaultPath(), cfg); err != nil {
			return err
		}
	case "run":
		return runAuto(api)
	}
	return o.autoStatus()
}

// SetAuto saves the mode and installs or removes the background service.
func SetAuto(on bool) error {
	path := auto.DefaultPath()
	cfg, err := auto.LoadConfig(path)
	if err != nil {
		return err
	}
	if !on {
		cfg.Mode = "manual"
		if err := auto.SaveConfig(path, cfg); err != nil {
			return err
		}
		return uninstallService()
	}
	exe, err := installedPath()
	if err != nil {
		if exe, err = executable(); err != nil {
			return err
		}
	}
	cfg.Mode = "auto"
	if err := auto.SaveConfig(path, cfg); err != nil {
		return err
	}
	if err := installService(exe); err != nil {
		cfg.Mode = "manual"
		return errors.Join(fmt.Errorf("auto mode stays off: %w", err), auto.SaveConfig(path, cfg), uninstallService())
	}
	return nil
}

func waitForService() {
	for end := time.Now().Add(serviceStartWait); !serviceRunning() && time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
	}
}

func runAuto(api freezer.API) error {
	// Locked before signals are caught, so Ctrl+C still ends a loop that is waiting.
	lock, err := auto.LockRun(func() {
		fmt.Fprintf(os.Stderr, "%s auto: waiting, auto mode already runs elsewhere (stop it with '%s auto off')\n", prog.Name, prog.Name)
	})
	if err != nil {
		return err
	}
	defer lock.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var sampler pressure.Sampler
	r := auto.Runner{
		API:    api,
		Read:   pressure.Read,
		CPU:    sampler.Sample,
		Notify: auto.Notify,
		Now:    time.Now,
		Log: func(e auto.Event) {
			if err := auto.AppendEvent(auto.LogPath(), e); err != nil {
				fmt.Fprintf(os.Stderr, "%s auto: log: %s\n", prog.Name, err)
			}
		},
	}
	return r.Run(ctx, auto.DefaultPath(), auto.WatchFocus(ctx))
}

func (o *output) autoStatus() error {
	cfg, err := auto.LoadConfig(auto.DefaultPath())
	if err != nil {
		return err
	}
	last, hasLast := auto.LastEvent(auto.LogPath())
	running := serviceRunning()
	if o.json {
		v := struct {
			auto.Config
			ServiceRunning bool        `json:"service_running"`
			LastEvent      *auto.Event `json:"last_event,omitempty"`
		}{Config: cfg, ServiceRunning: running}
		if hasLast {
			v.LastEvent = &last
		}
		o.writeJSON(v)
		return nil
	}
	service := "not running"
	if running {
		service = "running"
	}
	row := func(name, value string) { fmt.Fprintf(o.stdout, "%-10s%s\n", name, value) }
	row("mode", cfg.Mode)
	row("service", service)
	for _, s := range auto.Settings {
		row(s.Name, s.Display(cfg))
	}
	row("pinned", strconv.Itoa(len(cfg.Pins)))
	if hasLast {
		row("last", last.Time.Local().Format("Jan 2 15:04")+" "+last.String())
	}
	return nil
}

func settingNames() string {
	names := make([]string, 0, len(auto.Settings))
	for _, s := range auto.Settings {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
