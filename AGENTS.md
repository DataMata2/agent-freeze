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
| `afz list --json` | `{"groups":[Group...]}` |
| `afz freeze <sel>... \| --all [--include-daemons] [--yes] --json` | `{"results":[Result...],"freed_bytes":N}` |
| `afz thaw <sel>... \| --all --json` | `{"results":[Result...],"freed_bytes":0}` |
| `afz status --json` | `{"frozen":N,"freed_bytes":N,"frozen_swap_bytes":N,"mem_available_bytes":N,"swap_free_bytes":N}` |
| `afz auto [status] \| on \| off \| set <setting> <value> --json` | `{"mode":"manual\|auto","idle_minutes":N,"interval_seconds":N,"memory_level":"warn\|critical\|off","cpu_load":N,"cpu_use_percent":N,"away_minutes":N,"retry_minutes":N,"notify":B,"tab_thaw":B,"pins":[...],"service_running":B,"last_event":Event}` |
| `afz rename <name> --json` | `{"from":"<old path>","to":"<new path>"}` |
| `afz version --json` | `{"version":"..."}` |

A selector is a group `id`, a `key`, or a case-insensitive substring of `name`.

**Group:** `id` (root PID as string), `key`, `agent`, `name`, `session_id`
(optional), `cwd`, `root_pid`, `start_time`, `pids`, `rss_bytes`, `swap_bytes`,
`daemon`, `self`, `status` (`busy` | `idle` | `shell`; omitted if unknown),
`status_since` (optional, RFC 3339: when `status` last changed), `tty`
(optional, the agent's terminal), `state` (`running` | `frozen` | `partial`), `frozen_at` (optional, RFC 3339),
`freed_bytes`, `delegated`.

**Result:** `id`, `key`, `name`, `ok`, `error` (optional), `warning`
(optional), `rss_before`, `rss_after`, `swap_after`. Sizes are bytes.

**Settings** for `auto set`: `idle`, `interval`, `memory` (`warn` | `critical` |
`off`), `cpu-load` (0 = off), `cpu-use`, `away`, `retry`, `notify` (`on` |
`off`), `tab-thaw` (`on` | `off`). A bad value is a usage error that names the
allowed range.

**Event:** `time`, `action` (`froze` | `thawed`), `by` (`auto` | `manual`),
`name`, `reason` (optional), `rss_bytes` (optional), `error` (set when it
failed). `freeze` and `thaw` are recorded as `manual`. `last_event` and `pins`
are omitted when empty.

Errors: usage errors print `{"error":"..."}`; an operation error (e.g. not
enough swap) also prints `{"error":"..."}`.

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
