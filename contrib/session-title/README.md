# session-title: name Claude Code sessions after their task

An optional [Claude Code hook](https://docs.claude.com/en/docs/claude-code/hooks)
that gives each session a short name like `PROJ-42 login timeout` or
`Fix bottom sheet z-index`, and renames it when the work moves on. The names
show up in Claude Code's session list, in the terminal title and in the NAME
column of `afz`, so you can tell idle agents apart before you freeze them.

## When it names a session

- **The first 3 prompts** (`EARLY_PROMPTS`).
- **A ticket id not seen before in the session**: `PROJ-123` or `#123`
  (`TICKET_RE`). Look-alikes such as `UTF-8` or `SHA-256` are skipped
  (`NOT_TICKET_PREFIXES`).
- **Right after a compaction**, from the summary's "Current Work" and "Optional
  Next Step", so a long session is named after what it does now.

A rename by hand (`/rename`, `claude --name`) wins: the hook never touches that
session again. Claude Code's own automatic names are treated as placeholders and
replaced.

Each name costs one short `claude -p` call (Haiku by default, `MODEL`), billed to
the account the session runs in, and adds a few seconds while that prompt is sent.
Prompts get at most 12 calls per session (`MAX_RUNS`); compactions always
rename. The model call runs with no tools. If it fails, the prompt goes through
unnamed.

## Install

Needs `python3` and the `claude` CLI on your PATH.

```sh
mkdir -p ~/.claude/hooks
cp contrib/session-title/session-title.py ~/.claude/hooks/
chmod +x ~/.claude/hooks/session-title.py
```

Add both hooks to `~/.claude/settings.json` (merge them into an existing `hooks`
object if you have one):

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          { "type": "command", "command": "~/.claude/hooks/session-title.py", "timeout": 30, "statusMessage": "Naming session…" }
        ]
      }
    ],
    "SessionStart": [
      {
        "matcher": "compact",
        "hooks": [
          { "type": "command", "command": "~/.claude/hooks/session-title.py", "timeout": 30, "statusMessage": "Naming session…" }
        ]
      }
    ]
  }
}
```

A second account run with `CLAUDE_CONFIG_DIR=~/.claude-<name>` has its own
settings: add the same two hooks to `~/.claude-<name>/settings.json`.

## Make it yours

The settings sit at the top of `session-title.py`: `EARLY_PROMPTS`, `MAX_RUNS`,
`MODEL`, `MAX_TITLE_CHARS`, `TICKET_RE` (your tracker's id format) and
`NOT_TICKET_PREFIXES`. The naming rules for the model are in `ask_model`.

State lives in `<config dir>/session-titles/<session id>.json`. To stop, remove
the two hooks from your settings.

## Tests

```sh
python3 -m unittest discover -s contrib/session-title -p 'test_*.py'
```
