# afz for AI agents

`afz` freezes coding-agent process groups (cgroup v2 freezer on Linux,
`SIGSTOP` on macOS) and gets their memory out of RAM; `thaw` resumes them
exactly where they stopped. The command may have been renamed (`afz rename`);
use the name the user gave you. Always pass
`--json`: each call prints one JSON object on stdout. Bare `afz` with stdout
not a terminal behaves like `afz list` (text).

## Rules

- **Never pass `--force-self` unless the user explicitly asked.** Without it
  `afz` refuses to freeze your own group (`"self": true`), and `--all` skips it.
- Daemons (`"daemon": true`) are skipped by `freeze --all` unless
  `--include-daemons`; only add it if the user asked.
- Freezing a busy agent (`"status": "busy"`, mid-turn) is allowed, but its
  result gets the warning `was busy`; tell the user.
- If free swap is smaller than the targets' RAM, `freeze` fails; add `--yes`
  only if the user accepts that RAM will not be freed.
- `freeze` shows a "frozen" banner and window title in the agent's terminal;
  `thaw` clears it and sends the agent `SIGWINCH` to redraw. Banner problems
  are `banner: ...` warnings only. `AFZ_NO_BANNER=1` disables the banner.
- On macOS memory leaves RAM only once other apps need it, so `freed_bytes`
  right after a freeze is small; `afz status` shows it growing. `swap_bytes`
  is always 0 there.
- **Never run `rename` unless the user asked for that name.**
- **Never run `auto on`, `auto off` or `auto set` unless the user asked.**
  Auto mode freezes the user's unused sessions by itself.

## Commands

| Command | JSON output |
| --- | --- |
| `afz list [<query>...] --json` | `{"groups":[Group...]}` |
| `afz freeze <sel>... \| --all [--include-daemons] [--yes] --json` | `{"results":[Result...],"freed_bytes":N}` |
| `afz thaw <sel>... \| --all --json` | `{"results":[Result...],"freed_bytes":0}` |
| `afz status --json` | `{"frozen":N,"freed_bytes":N,"frozen_swap_bytes":N,"mem_available_bytes":N,"swap_free_bytes":N}` |
| `afz auto [status] \| on \| off \| set <setting> <value> --json` | `{"mode":"manual\|auto","idle_minutes":N,"interval_seconds":N,"memory_level":"warn\|critical\|off","cpu_load":N,"cpu_use_percent":N,"away_minutes":N,"retry_minutes":N,"notify":B,"tab_thaw":B,"input_thaw":B,"pins":[...],"service_running":B,"last_event":Event}` |
| `afz rename <name> --json` | `{"from":"<old path>","to":"<new path>"}` |
| `afz version --json` | `{"version":"..."}` |

A selector is a group `id`, a `key`, or a case-insensitive substring of `name`.
A `list` query keeps groups whose name, cwd, agent or `id` contains it
(case-insensitive; several are OR'd; no match is `{"groups":[]}`, exit 0).

**Group:** `id` (root PID as string), `key`, `agent`, `name`, `session_id`
(optional), `cwd`, `root_pid`, `start_time`, `pids`, `rss_bytes`, `swap_bytes`,
`daemon`, `self`, `status` (`busy` | `idle` | `shell` | `waiting`; omitted if unknown),
`status_since` (optional, RFC 3339: when `status` last changed), `tty`
(optional, the agent's terminal), `state` (`running` | `frozen` | `partial`), `frozen_at` (optional, RFC 3339),
`frozen_by` (optional, `"auto"` when auto mode froze it), `freed_bytes`, `delegated`.

**Result:** `id`, `key`, `name`, `ok`, `error` (optional), `warning`
(optional), `rss_before`, `rss_after`, `swap_after`. Sizes are bytes.

**Settings** for `auto set`: `idle`, `interval`, `memory` (`warn` | `critical` |
`off`), `cpu-load` (0 = off), `cpu-use`, `away`, `retry`, `notify` (`on` |
`off`), `tab-thaw` (`on` | `off`, macOS), `input-thaw` (`on` | `off`, Linux).
A bad value is a usage error that names the
allowed range.

**Event:** `time`, `action` (`froze` | `thawed` | `failed` | `skipped`), `name`, `reason`
(optional), `rss_bytes` (optional), `error` (optional). `last_event` and
`pins` are omitted when empty.

Errors: usage errors print `{"error":"..."}`; an operation error (e.g. not
enough swap) also prints `{"error":"..."}`.

## Auto mode

A background service (launchd agent on macOS, systemd user unit
`agent-freeze-auto.service` on Linux) checks every `interval` seconds and,
under memory or CPU pressure, freezes at most one unused session per check.

- Only `"status": "idle"` sessions qualify. `shell` (no turn runs, but a
  background Bash or Monitor task does), `waiting` (blocked on the
  user), `busy`, unknown status, `self`, daemons, pinned groups and groups
  not `running` are never auto-frozen. So the CPU path rarely finds a
  candidate: an idle session seldom uses much CPU.
- Before a freeze it lists again and re-checks the group. On Linux a session
  with typed input it has not read yet is left alone. If free swap is
  smaller than its RAM it logs a `skipped` event (and, if notify is on,
  notifies, once until a check sees no pressure). Either way
  that session is tried again after `retry` minutes.
- After an auto freeze it waits max(2 intervals, 60 s), counted from the
  newest auto freeze (also one from before a restart), and then needs a new
  reading that still shows pressure.
- Groups it froze have `"frozen_by": "auto"`. Tab-thaw (macOS) and
  input-thaw (Linux) thaw only those; a manual freeze is never auto-thawed,
  and freezing an auto-frozen group by hand makes it a manual freeze.
  Thawing an auto-frozen group by hand (`thaw`) restarts its idle time at
  the next check (groups with a pty only).
- On Linux, input-thaw (default on) watches the ptys of auto-frozen sessions
  and thaws on any input byte (raw mode): typing, pastes, mouse events and
  terminal focus reports; idle time then starts again. A session also counts
  as used when it last read input from its pty (the pty's atime, in 8-second
  steps; any input it reads counts, terminal replies too). `away` applies on
  macOS only.
- Notifications use `notify-send` on Linux; without it the service warns once
  on stderr and sends none. The unit copies `CLAUDE_CONFIG_DIR` and
  `AFZ_NO_BANNER=1` from the environment of `afz auto on` when set; `rename`
  rewrites the unit from its own environment, so a variable not set there
  drops out (run `afz auto on` again with it set).
- Input-thaw does not work for Claude background sessions
  (`claude --bg-pty-host`): the host that forwards typing is frozen with
  the group. Thaw those with `thaw`.

## Exit codes

- `0` every target succeeded (an empty target set is success)
- `1` at least one target failed (see `ok`/`error`), or the operation failed
- `2` usage error: unknown command or flag, unknown or ambiguous selector

## Example: free RAM for a game

```sh
afz freeze --all --json   # everything except you and daemons; report freed_bytes
# ... user plays ...
afz thaw --all --json     # restore every frozen or partial group
```
