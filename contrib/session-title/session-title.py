#!/usr/bin/env python3
"""Claude Code hook: gives each session a short name that says what it is working on.

UserPromptSubmit: names the session on its first prompts, and again whenever a new
ticket id shows up. SessionStart with source "compact": renames it from the
compaction summary, so a long session follows its current work. A rename by hand
(/rename, --name) wins: the hook never touches that session again.

Install and settings: see README.md next to this file.
"""
import json
import os
import re
import subprocess
import sys
from pathlib import Path

# Settings: change these to fit how you work.
EARLY_PROMPTS = 3  # the first prompts always get a name
MAX_RUNS = 12  # most model calls per session
MODEL = "haiku"  # the model that picks the name
MAX_TITLE_CHARS = 60
# A ticket id not seen before in the session starts a rename: PROJ-123 or #123.
TICKET_RE = re.compile(r"\b[A-Z][A-Z0-9]{1,9}-\d{1,6}\b|(?<!\w)#\d{2,5}\b")
# Prefixes that look like ticket ids but are not (UTF-8, SHA-256, GPT-4).
NOT_TICKET_PREFIXES = {"AES", "GPT", "HTTP", "ISO", "PEP", "RFC", "RSA", "SHA", "SSL", "TLS", "UTF"}

GUARD_ENV = "CLAUDE_SESSION_TITLE_HOOK"
CHATTER = re.compile(r"(?i)^(here\b|title\b|sorry\b|i\s|i'm\b|as an\b)")
NOISE_TAGS = re.compile(
    r"<(ide_opened_file|ide_selection|system-reminder|command-message|command-name|command-args|local-command-stdout)>.*?</\1>",
    re.S,
)


def config_dir() -> Path:
    return Path(os.environ.get("CLAUDE_CONFIG_DIR") or Path.home() / ".claude")


def ticket_ids(text: str) -> list:
    return [t for t in TICKET_RE.findall(text) if t.split("-")[0] not in NOT_TICKET_PREFIXES]


def summary_section(text: str, heading: str) -> str:
    match = re.search(rf"{heading}:(.*?)(?=\n\d+\. [A-Z][^\n]*:|\Z)", text, re.S)
    return match.group(1) if match else ""


def clean_prompt(prompt: str) -> str:
    text = NOISE_TAGS.sub(" ", prompt)
    if text.lstrip().startswith("This session is being continued"):
        text = summary_section(text, "Primary Request and Intent") or text
    return re.sub(r"\s+", " ", text).strip()[:1500]


def compact_summary(transcript_path: str) -> str:
    last = ""
    with open(transcript_path, encoding="utf-8") as transcript:
        for line in transcript:
            if '"isCompactSummary"' in line:
                last = line
    if not last:
        return ""
    content = json.loads(last)["message"]["content"]
    if isinstance(content, list):
        content = " ".join(part.get("text", "") for part in content if isinstance(part, dict))
    now = summary_section(content, "Current Work") + " " + summary_section(content, "Optional Next Step")
    return re.sub(r"\s+", " ", now).strip()[:1500]


def session_name(session_id: str) -> tuple:
    for path in (config_dir() / "sessions").glob("*.json"):
        try:
            info = json.loads(path.read_text())
        except (OSError, ValueError):
            continue
        if info.get("sessionId") == session_id:
            return info.get("name") or "", info.get("nameSource") or ""
    return "", ""


def load_state(path: Path) -> dict:
    try:
        state = json.loads(path.read_text())
    except (OSError, ValueError):
        state = {"titles": [], "runs": 0, "locked": False}
    return {"seen_ids": [], **state}


def save_state(path: Path, state: dict) -> None:
    tmp = path.with_name(f"{path.name}.{os.getpid()}.tmp")
    tmp.write_text(json.dumps(state))
    os.replace(tmp, path)


def ask_model(current: str, label: str, message: str, cwd: str) -> str:
    instruction = (
        "Name this coding session so the user can find it later in a session list.\n"
        "Rules: 2-6 words, plain text, no quotes, no trailing period. "
        "If a ticket id like PROJ-123 or #123 is mentioned, start with it, copied character for character "
        "('#253' stays '#253', never 'PROJ-253'). "
        "Name the task, not the chat (e.g. 'PROJ-42 login timeout', 'Fix bottom sheet z-index'). "
        "If the current title still fits, return it unchanged. Output only the title.\n\n"
        f"Project folder: {os.path.basename(cwd or '')}\n"
        f"Current title: {current or '(none)'}\n"
        f"{label}: {message}"
    )
    env = {**os.environ, GUARD_ENV: "1"}
    result = subprocess.run(
        ["claude", "-p", "--model", MODEL, "--no-session-persistence",
         "--setting-sources", "", "--strict-mcp-config", "--tools", ""],
        input=instruction, capture_output=True, text=True, timeout=25, env=env,
        cwd=config_dir() / "session-titles",
    )
    lines = [line for line in result.stdout.strip().splitlines() if line.strip()]
    return lines[0] if result.returncode == 0 and lines else ""


def sanitize(title: str) -> str:
    title = re.sub(r"[\x00-\x1f\x7f]", "", title)
    title = re.sub(r"^(?:[\"'`*\s]|#+\s)+|[\"'`*.\s]+$", "", title)
    if not title or len(title.split()) > 8 or title.endswith(":") or CHATTER.match(title):
        return ""
    return title[:MAX_TITLE_CHARS]


def restore_ids(title: str, message: str) -> str:
    return re.sub(
        r"\b[A-Z][A-Z0-9]+-(\d+)\b",
        lambda m: f"#{m.group(1)}" if m.group(0) not in message and f"#{m.group(1)}" in message else m.group(0),
        title,
    )


def main() -> None:
    if os.environ.get(GUARD_ENV):
        return
    payload = json.load(sys.stdin)
    session_id = payload.get("session_id")
    if not session_id:
        return
    event = payload.get("hook_event_name") or "UserPromptSubmit"
    if event == "SessionStart":
        if payload.get("source") != "compact":
            return
        label, message = "What the session is doing now", compact_summary(payload.get("transcript_path") or "")
    else:
        label, message = "Latest user message", clean_prompt(payload.get("prompt") or "")
    if len(message) < 3:
        return

    name, source = session_name(session_id)
    current = (payload.get("session_title") or name).strip()
    state_dir = config_dir() / "session-titles"
    state_dir.mkdir(parents=True, exist_ok=True)
    state_path = state_dir / f"{session_id}.json"
    state = load_state(state_path)
    if state["locked"]:
        return
    # Claude Code's own "derived" name is a placeholder; only a hand rename locks.
    # Without nameSource, a name differing from the hook's own titles means a hand rename.
    renamed_by_hand = source == "user" or (
        not source and state["titles"] and current and current not in state["titles"]
    )
    if renamed_by_hand:
        save_state(state_path, {**state, "locked": True})
        return

    new_ids = [t for t in ticket_ids(message) if t not in state["seen_ids"] and t not in ticket_ids(current)]
    state = {**state, "seen_ids": state["seen_ids"] + new_ids}
    # Compactions are rare, so they always rename; prompts share the MAX_RUNS budget.
    if event != "SessionStart" and (
        state["runs"] >= MAX_RUNS or (state["runs"] >= EARLY_PROMPTS and not new_ids)
    ):
        save_state(state_path, state)
        return

    title = restore_ids(sanitize(ask_model(current, label, message, payload.get("cwd", ""))), message)
    renamed = bool(title) and title != current
    state = {**state, "runs": state["runs"] + 1, "titles": state["titles"] + ([title] if renamed else [])}
    save_state(state_path, state)
    if renamed:
        print(json.dumps({"hookSpecificOutput": {"hookEventName": event, "sessionTitle": title}}))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:  # a name is never worth blocking the prompt
        print(f"session-title hook: {error}", file=sys.stderr)
    sys.exit(0)
