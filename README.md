# agent-freeze

Like a Fallout vault for your coding agents: `afz` puts idle Claude Code,
Codex and friends into cryo so a game can have the RAM, then wakes them up
exactly where they left off.

Too many Claude Code sessions open and your laptop starts to lag? Every
session keeps its process, tool shells and MCP servers in memory, even while
it sits idle. `afz` pauses the sessions you are not using and gives their RAM
back to everything else, on Linux and macOS. On a 16 GB MacBook running eight
Claude Code sessions, freezing the three idle ones gave back about 490 MB
within a few minutes.

`afz` lists every coding-agent process group on the machine, freezes the ones
you pick (with the cgroup v2 freezer on Linux, with signals on macOS), gets
their memory out of RAM, and thaws them later. No root needed. A thawed agent
continues from the exact point it stopped, even mid-turn.

Recognised agents: Claude Code (`claude`), OpenAI Codex CLI (`codex`), Gemini
CLI (`gemini`), `opencode`, Aider (`aider`), Cursor CLI (`cursor-agent`) and
Amp (`amp`), also when run by `node`, `bun`, `deno` or `python` (by script
name or a `node_modules/<agent>` path).

## Install

```sh
go install github.com/DataMata2/agent-freeze/cmd/afz@latest
```

Builds without a version stamp, including `go install`, report `afz dev`. For
a versioned build:

```sh
go build -ldflags "-X github.com/DataMata2/agent-freeze/internal/cli.Version=v0.1.0" -o afz ./cmd/afz
```

`go install` puts `afz` in `$(go env GOPATH)/bin` (usually `~/go/bin`); add
that directory to your `PATH` if your shell cannot find it.

### Pick your own name

The command answers to whatever its file is called, in its help, its errors
and the frozen banner. Rename it in place:

```sh
afz rename chill    # from now on: chill list, chill freeze --all, ...
```

It refuses a name that is already a command or a file. If `afz` is a
symlink, the link is renamed and the program stays where it is. A later
`go install` writes a new `afz` next to it; delete the old `chill` and rename
again.

## Requirements

Linux:

- The cgroup v2 unified hierarchy at `/sys/fs/cgroup`
  (`afz` exits with `afz: cgroup v2 not available` otherwise).
- A systemd user session: agents must run inside your delegated
  `user@<uid>.service` subtree (the normal case for terminals started from a
  desktop session), with the `memory` controller available.
- Swap. Freezing works without it, but then no RAM is freed.
- Kernel 5.19 or newer for `memory.reclaim`.

macOS: nothing beyond a stock install. `afz` reads the process table from the
kernel and uses the system's `ps` and `lsof`.

## Usage

### TUI

Run `afz` with no arguments in a terminal:

```sh
afz
```

The header shows the mode (manual or auto), available RAM, free swap, the
frozen count and total freed memory. Each row is one agent group with its state (running, frozen, partial),
status (busy, idle, shell, waiting), RAM, swap and process count. Your own group is
marked `(you)`.

| Key | Action |
| --- | --- |
| `↑`/`↓`, `k`/`j` | move |
| `space` | toggle selection |
| `a` | select all (except yourself and daemons) |
| `f` | freeze the selected rows, or the current row |
| `u` | thaw the selected rows, or the current row |
| `F` | freeze all (except yourself and daemons) |
| `U` | thaw all |
| `/` | search: type to filter rows by name, cwd, agent or ID; `Enter` keeps the filter, `Esc` clears it |
| `p` | pin or unpin the current row: auto mode never freezes a pinned row |
| `m` | switch between manual and auto mode |
| `s` | auto mode settings: `←`/`→` change, `enter` type a value, `s`/`esc` back |
| `r` | refresh |
| `q` | quit |

With a filter active, `a`, `f`, `u`, `F` and `U` act on the visible rows
only; a selected row that the filter hides stays selected but is not touched.
If every selected row is hidden, `f` and `u` do nothing.

When stdout is not a terminal (a pipe, or an AI agent calling it), bare `afz`
prints `afz list` instead of opening the TUI.

### CLI

```sh
afz list                         # ID NAME STATE STATUS RAM SWAP PROCS CWD
afz list --all-procs             # also print each group's PIDs
afz list acme src/api            # only groups matching any query
afz freeze --all                 # freeze every agent except you and daemons
afz freeze 540032 my-project     # freeze by ID, key or name substring
afz thaw --all                   # thaw everything frozen
afz status                       # frozen totals, available RAM, free swap
afz auto on                      # auto mode, see below
afz rename chill                 # call the command chill from now on
afz version
afz help
```

A selector is a group ID (the root PID), a group key, or a case-insensitive
substring of the name. An exact ID or key wins; a substring matching several
groups is an error that lists the candidates.

A `list` query is a case-insensitive substring of the name, the cwd (full or
`~`-shortened), the agent or the ID; several queries are OR'd. No match is an
empty list, not an error.

Flags:

| Flag | Commands | Meaning |
| --- | --- | --- |
| `--json` | all but `help` | one JSON object on stdout |
| `--all` | `freeze`, `thaw` | every eligible group instead of selectors |
| `--include-daemons` | `freeze` | let `--all` include agent daemons |
| `--yes` | `freeze` | proceed even if free swap is smaller than the targets' RAM |
| `--force-self` | `freeze` | allow freezing the group `afz` runs in |
| `--all-procs` | `list` | print each group's PIDs |

Exit codes: `0` success, `1` at least one target failed (or the operation
failed), `2` usage error, unknown or ambiguous selector.

AI agents calling `afz` should read [AGENTS.md](AGENTS.md).

### Claude Code skill

[`skills/afz/SKILL.md`](skills/afz/SKILL.md) tells Claude Code sessions that
`afz` exists and how to use it safely, so a session can answer "what is using
my RAM?" or "park the idle sessions" by itself. Install it from a clone:

```sh
mkdir -p ~/.claude/skills/afz && cp skills/afz/SKILL.md ~/.claude/skills/afz/
```

or without one:

```sh
mkdir -p ~/.claude/skills/afz && curl -fsSL https://raw.githubusercontent.com/DataMata2/agent-freeze/main/skills/afz/SKILL.md -o ~/.claude/skills/afz/SKILL.md
```

The skill calls the command `afz`; if you renamed it, tell the session the
new name.

### Auto mode

In auto mode `afz` watches the machine and freezes unused sessions for you
when it runs short of memory or CPU. A background service does the work, so
it runs with the TUI closed and starts again at login.

```sh
afz auto on            # turn it on (or press m in the TUI)
afz auto set idle 5    # freeze sessions unused for 5 minutes or more
afz auto               # mode, service, every setting and the last thing it did
afz auto off           # back to manual; stops and removes the service
```

Every setting can be changed with `afz auto set <setting> <value>` or on the
TUI's settings screen (`s`). The running service picks changes up at its next
check.

| Setting | Default | Meaning |
| --- | --- | --- |
| `idle` | `15` | minutes a session must be unused before a freeze (0 to 10080) |
| `interval` | `30` | seconds between checks (5 to 3600) |
| `memory` | `warn` | memory pressure that starts freezing: `warn`, `critical` or `off` |
| `cpu-load` | `1` | load per CPU core that starts freezing; `0` turns the CPU check off |
| `cpu-use` | `10` | % of one core an unused session must use to be frozen for CPU |
| `away` | `5` | minutes a session must be unused while the selected tab is unknown (macOS) |
| `retry` | `30` | minutes before a session that failed to freeze, or was skipped, is tried again |
| `notify` | `on` | a notification on each auto freeze, and on the first skipped freeze under pressure |
| `tab-thaw` | `on` | selecting an auto-frozen session's Terminal tab thaws it (macOS) |
| `input-thaw` | `on` | typing into an auto-frozen session's terminal thaws it (Linux) |

`auto off` does not thaw what auto mode froze; thaw those with `u` or
`afz thaw`.

At each check it reads the memory pressure (macOS
`kern.memorystatus_vm_pressure_level`, Linux `/proc/pressure/memory`) and the
load. Under memory pressure it freezes one unused session, the one unused the
longest (then the largest). When the CPU is busy, it freezes the unused
session that uses the most CPU. One session per check, never more.

A session is unused when Claude Code reports it `idle` for at least the idle
time, counted from its last turn, from when you last had its tab selected
(macOS), or from when it last read input from its terminal (Linux),
whichever is later. Only `idle` counts: a session at `shell` (no turn runs,
but a background Bash or Monitor task does) or `waiting` (blocked on you) is
never frozen. Neither is a busy session, the session running `afz`, a
daemon, a pinned session (`p` in the TUI), or an agent with no status (only
Claude Code reports one). Sessions running a dev server are not exempt: pin
them if they must keep running. Since an idle session rarely uses much CPU,
the CPU trigger seldom finds one to freeze; memory pressure is the main
trigger.

Right before a freeze, auto mode lists the sessions again and checks the
chosen one once more, since it may have started a turn. If free swap is
smaller than the session's RAM it skips the freeze, logs it, and, if notify
is on, notifies you (once until a check sees no pressure); a session it
skips or fails to freeze is left alone for the retry time. After an auto
freeze it waits two check intervals, and at least 60 seconds, so the freed
memory shows in the next reading, and freezes again only if that reading
still shows pressure. This wait also covers a freeze
made before the service restarted.

`afz list --json` shows `"frozen_by": "auto"` on sessions auto mode froze.
Only those thaw by themselves (tab on macOS, typing on Linux). A manual
freeze stays until `u` or `afz thaw`, and freezing an auto-frozen session by
hand makes it a manual freeze. Thawing an auto-frozen session with `u` or
`afz thaw` starts its idle time again at the next check, so it is not frozen
again at once.

#### On macOS

Auto mode also watches Apple Terminal: the session in the selected tab is
never frozen, and a session it froze thaws as soon as you select its tab.
The first time, macOS asks whether `afz` may control Terminal; allow it.
While the tab cannot be read (permission denied, another app in front), a
session must also be unused for the away time. Inside tmux, or in another
terminal app, the selected tab cannot be matched to a session: pin the
sessions you are working in, or raise the idle time. Notifications use
`osascript`.

#### On Linux

Auto mode watches the terminals of the sessions it froze: as soon as you
type into one, that session thaws and reads what you typed, and its idle
time starts again (turn this off with `afz auto set input-thaw off`). It
only looks at whether unread input is waiting and never reads it, so
nothing you type is lost. Input counts as soon as one byte arrives in raw
mode, which agents use; in a terminal in canonical (line) mode it counts
only after Enter. Anything the terminal sends counts as typing: pastes,
mouse events, and the focus reports some terminals send when you switch to
their window. A session with typed input it has not read yet is never
frozen.

Typing into a running session counts as use too: the kernel records when a
terminal was last read from (in 8-second steps), so idle time counts from
the last time the agent read input. Any input the agent reads counts,
including replies the terminal sends to its queries. There is no tab
detection on Linux, so the `away` setting does not apply.

Notifications use `notify-send`; if it is not installed, the service warns
once in its log and sends none. The systemd unit restarts the service 5
seconds after a failure. `afz auto on` writes `CLAUDE_CONFIG_DIR` and
`AFZ_NO_BANNER=1` into the unit when they are set in its environment; after
changing either, run `afz auto on` again. The service log is
`journalctl --user -u agent-freeze-auto.service`.

#### Not covered

- No window manager integration on Linux: auto mode does not know which
  window has focus. Switching to a terminal thaws its session only through
  the input the terminal sends (a focus report, if the agent asked for
  them). Thawing on window focus (for example under Hyprland) is a possible
  follow-up.
- A session with no terminal that you thaw with `u` or `afz thaw` may be
  auto-frozen again after the cooldown if the pressure holds and it stays
  idle; pin it. (With a terminal, its idle time starts again.)
- Auto freezes made by a version without `frozen_by` look manual after an
  upgrade and never thaw by themselves; thaw them with `u` or `afz thaw`.

#### The service

The service is a launchd agent on macOS
(`~/Library/LaunchAgents/io.github.datamata2.agent-freeze.auto.plist`) and a
systemd user unit on Linux (`agent-freeze-auto.service`). It runs the command
from where it is installed; `afz rename` moves it along, but after moving the
file yourself run `afz auto on` again. Settings live in `auto.json` and the
history in `auto.log` next to the state file; `afz auto run` runs the loop in
the foreground.

## How it works

An agent group is the agent's root process plus all its descendants (tool
shells, MCP servers, and so on). Names come from Claude Code's session files
(`~/.claude/sessions`), from `-n`/`--name`, or from the session ID, falling
back to `<agent> @ <basename of cwd>`. Daemons are named like `claude daemon`
or `claude bg-spare`.

### On Linux

**Freeze.**

1. `afz` creates a cgroup for the group under
   `user@<uid>.service/app.slice/agent-freeze/<key>` and moves every process of
   the group into it, re-scanning for processes spawned during the move. Only
   the agent tree moves: the terminal emulator and your shell stay in their
   own scope, so the window stays responsive. This is allowed without root
   because the whole subtree belongs to you.
2. It writes `1` to the new cgroup's `cgroup.freeze` and waits for
   `cgroup.events` to report `frozen 1`. Unlike SIGSTOP, the freezer is
   invisible to the frozen processes and does not trigger the parent shell's
   job control.
3. It reclaims the memory with `memory.reclaim` (`swappiness=200`, so the
   kernel evicts anonymous memory to swap; on kernels that reject that key it
   falls back to plain reclaim). In cgroup v2 a process's pages
   stay charged to the cgroup it was in when they were touched, not the one
   it moves to. So reclaim targets the **origin** cgroups recorded before the
   move (usually the terminal's scope), asking each for the RSS its former
   members still hold, and also the new cgroup for pages touched after the
   move. A single `memory.reclaim` write takes pages from the whole scope, so
   one pass may free only part of the agent's memory. `afz` therefore repeats
   it, up to 4 rounds, until less than 5% of the RSS is left or a round frees
   under 16 MiB. The frozen pages are never touched again, so each round ages
   them further while the terminal's own pages stay hot.

The origin cgroup of every PID is recorded in
`$XDG_STATE_HOME/agent-freeze/state.json` (default
`~/.local/state/agent-freeze/state.json`). Whether a group is frozen is always
read from the cgroup tree, never from that file.

**Thaw.** `afz` writes `0` to `cgroup.freeze`, moves every process back to the
cgroup it came from, and removes the empty agent-freeze cgroup. If a process's
origin cgroup is gone, it goes to the root process's origin; failing that, it
stays where it is with a warning, and the state entry is kept so a later thaw
can retry. Moving back
matters: pages swapped back in are charged to the origin again, so the next
freeze must start from there to reclaim them.

**Banner.** A frozen agent looks hung, so after a successful freeze `afz`
writes a reverse-video line to the bottom row of the agent's terminal,
`❄ frozen by afz at 15:04 — keystrokes are queued until thaw · don't close
this window`, and sets the window title to `❄ FROZEN — <name>`, first saving
the current title on the terminal's title stack. The terminal
is the one on the agent's stdin, stdout or stderr (for a Claude background
session, its claude process's); an agent without a terminal gets no banner.
After a successful thaw `afz` clears that line, sets the title to the group's
name, then restores the saved title (terminals without a title stack keep the
name), and sends the agent `SIGWINCH` so it redraws its whole screen. Writes
never block: if the terminal is not accepting output, the banner is skipped.
A banner problem only adds a `banner: ...` warning; the freeze or thaw still
succeeds. Set `AFZ_NO_BANNER=1` to turn the banner (and the `SIGWINCH`) off.

### On macOS

macOS has no cgroup freezer and no way to push another process's memory out
on demand, so a freeze works with signals:

1. **Freeze** sends `SIGSTOP` to every process of the group, re-scanning for
   processes spawned meanwhile, and waits until all of them are stopped. If
   the agent is the foreground job of an interactive shell (a Terminal,
   iTerm2 or tmux tab), that shell is stopped first: a stopped job would
   make the shell print `suspended` and take the terminal back, but a
   stopped shell cannot notice. So the tab behaves as on Linux and never
   needs `fg`. A job started with `&` leaves its shell running, since that
   shell is at the prompt.
2. **Memory** is not reclaimed at once. The stopped processes never touch
   their pages again, so macOS compresses or swaps them out as soon as other
   apps need RAM. `freed` (in `afz status`, the TUI header and
   `afz list --json`) is the RSS the group lost since the freeze, so it is
   small right after a freeze and grows from there.
3. **Thaw** sends `SIGCONT` to the helpers, then the agent, then the shell,
   so the shell runs again only once its job runs. The banner works as on
   Linux.

Every stopped PID is recorded with its start time in the state file before it
is signalled. Each run of `afz` ends the freezes that are over: if the agent
exited while frozen (an editor killed it), its stopped helpers and its shell
are continued; if it was continued by hand, the entry is dropped. Only groups
`afz` froze count as frozen, so an agent stopped with Ctrl+Z stays `running`.

`SWAP` is always `0B`: macOS does not report swap per process. `swap free` is
the free swap plus the free space of the swap volume (`/System/Volumes/VM`),
because macOS creates swap files as it needs them.

## What happens if I type into a frozen session

- **Keys are queued.** The terminal keeps accepting input, but the frozen
  agent does not read it, so nothing is echoed. Everything you type is
  delivered when you thaw (up to the terminal's input buffer, a few KiB).
- **Ctrl+C is queued too.** It does not interrupt or unfreeze the agent; it
  takes effect after thaw, as if you had pressed it then.
- **Don't close the window.** Closing it hangs up the terminal. The frozen
  agent cannot react yet, but the hang-up is delivered on thaw and ends the
  session. If that happens, `claude --resume` in the same directory recovers
  the conversation history.

## Safety

- **Self-exclusion.** `afz` never freezes the agent that runs it: the group
  containing any of `afz`'s ancestor processes is marked `(you)` / `"self": true`,
  is skipped by `--all`, and refuses to freeze by name unless `--force-self` is
  given. An agent should only pass `--force-self` when you explicitly ask it to.
- **Daemons.** Background helpers (`claude daemon`, `claude bg-spare`,
  `codex app-server`, ...) are marked `[daemon]` and excluded from
  `freeze --all` unless `--include-daemons` is given, because one daemon may
  host several sessions.
- **Swap check.** If the targets' RAM exceeds free swap, `afz freeze` refuses
  unless `--yes` is given.
- **Delegation.** Processes outside your `user@<uid>.service` subtree cannot be
  moved without root; such groups fail with
  `not in user-delegated cgroup`. On macOS a group with another user's
  processes fails with `process <pid> belongs to another user`.

## Caveats

- **No reboot survival.** Frozen agents live in RAM and swap only. A reboot
  ends them like any other process, and `afz` drops their stale state.
- **Busy agents.** Freezing an agent that is mid-turn is allowed but warned
  about: the CLI adds a `was busy` warning to the result, and the TUI asks for
  confirmation first. An API request in flight while frozen may time out and
  be retried by the agent after thaw.
- **zram vs disk swap (Linux).** zram keeps the "swapped" pages compressed in
  RAM, so it frees only part of the memory. A swap file or partition on disk
  frees the most.
- **Terminal paging (Linux).** Reclaim from the origin scope works on the
  whole terminal scope, so it can page out a little of the terminal emulator
  too. It pages back in as soon as you use that window.

## License

MIT, see [LICENSE](LICENSE).
