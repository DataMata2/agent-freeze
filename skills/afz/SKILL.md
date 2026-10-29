---
name: afz
description: Use when the user asks about system RAM, asks to pause, park, freeze or resume other coding-agent sessions, or the machine is low on memory. Requires the afz command to be installed.
---

# afz

`afz` freezes coding-agent process groups (Claude Code, Codex and others) and
gets their memory out of RAM: the cgroup v2 freezer plus swap on Linux,
`SIGSTOP` on macOS. `thaw` resumes them exactly where they stopped, even
mid-turn.

The command may have been renamed (`afz rename`); if the user gave you
another name for it, use that name everywhere this skill says `afz`. Check
that it is installed with `command -v afz`. If it is not installed, say so
and don't use afz; do not install it unless the user asks.

## Commands

Always pass `--json`; each call prints one JSON object on stdout.

| Command | Output |
| --- | --- |
| `afz list --json` | `{"groups":[Group...]}` |
| `afz list <query>... --json` | same; only groups whose name, cwd, agent or `id` contains a query (case-insensitive; no match is `{"groups":[]}`, exit 0) |
| `afz freeze <sel>... --json` | `{"results":[Result...],"freed_bytes":N}` |
| `afz freeze --all --json` | same; skips your own group and daemons |
| `afz thaw <sel>... --json` or `afz thaw --all --json` | `{"results":[Result...],"freed_bytes":0}` |
| `afz status --json` | `{"frozen":N,"freed_bytes":N,"frozen_swap_bytes":N,"mem_available_bytes":N,"swap_free_bytes":N}` |
| `afz auto --json` | auto mode: `mode` (`manual` \| `auto`), every setting, `service_running`, `last_event` |
| `afz auto on \| off --json`, `afz auto set <setting> <value> --json` | same, after the change |
| `afz rename <name> --json` | `{"from":"<old path>","to":"<new path>"}` |
| `afz version --json` | `{"version":"..."}` |

A selector is a group `id`, a `key`, or a case-insensitive substring of
`name`. Sizes are bytes.

Group fields that matter: `id`, `name`, `cwd`, `rss_bytes`, `swap_bytes`,
`state` (`running` | `frozen` | `partial`), `status` (`busy` | `idle` |
`shell` | `waiting`; omitted if unknown), `daemon`, `self`, `frozen_at`,
`frozen_by` (`"auto"` when auto mode froze it), `freed_bytes`.

Result fields: `id`, `key`, `name`, `ok`, `error`, `warning`, `rss_before`,
`rss_after`, `swap_after`.

Exit codes: `0` every target succeeded (an empty target set is success); `1`
at least one target failed (see `ok` and `error`) or the operation failed;
`2` usage error, or an unknown or ambiguous selector. Errors print
`{"error":"..."}`.

## Rules

- Listing and `status` are read-only and always fine.
- Freezing is the user's call. Freeze only what the user asked for. If they
  ask for memory in general, show them the list and ask which sessions to
  freeze before freezing any they did not mention. Another session may be
  someone's active work.
- If memory is low and the user hasn't asked, at most mention that afz is
  available. Never freeze anything on your own.
- Never pass `--force-self` unless the user explicitly asked. Without it `afz`
  refuses to freeze your own group (`"self": true`), and `--all` skips it.
- Daemons (`"daemon": true`) are skipped by `freeze --all` unless
  `--include-daemons` is given; add it only if the user asked. One daemon may
  host several sessions.
- Freezing a busy agent (`"status": "busy"`, mid-turn) is allowed, but its
  result gets the warning `was busy`; tell the user.
- If free swap is smaller than the targets' RAM, `freeze` fails. Add `--yes`
  only if the user accepts that RAM will not be freed.
- Never run `rename` unless the user asked for that name.
- Never run `auto on`, `auto off` or `auto set` unless the user asked. Auto
  mode freezes the user's unused (`idle`) sessions by itself under memory or
  CPU pressure, and by default thaws a session it froze when the user
  selects its tab (macOS) or types into it (Linux).

## What frozen means

A frozen session keeps its full state and continues from the same point after
`thaw`. Its terminal shows a "frozen" banner and window title; `thaw` clears
them and makes the agent redraw. Keystrokes typed into a frozen window are
queued until thaw. Closing a frozen window ends that session on thaw, so warn
the user not to close it. Banner problems only add a `banner: ...` warning.

On macOS memory leaves RAM only once other apps need it, so `freed_bytes`
right after a freeze is small; `afz status` shows it growing. `swap_bytes` is
always 0 there.

## Reporting

Tell the user which sessions were frozen or thawed by `name`, and how much
RAM was freed (`freed_bytes`, in MB or GB). On macOS, say that the number
grows as macOS reclaims the memory. Mention any result with `"ok": false` and
its `error`, and any `warning`.
