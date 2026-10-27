import importlib.util
import io
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("session_title", HERE / "session-title.py")
st = importlib.util.module_from_spec(spec)
spec.loader.exec_module(st)


def run_hook(payload, title="PROJ-1 new name"):
    out = io.StringIO()
    with mock.patch.object(st, "ask_model", return_value=title) as ask, \
            mock.patch("sys.stdin", io.StringIO(json.dumps(payload))), \
            mock.patch("sys.stdout", out):
        st.main()
    return out.getvalue(), ask


def printed_title(out):
    return json.loads(out)["hookSpecificOutput"]["sessionTitle"]


class HelpersTest(unittest.TestCase):
    def test_ticket_ids_match_jira_style_and_hash_numbers_only(self):
        found = st.ticket_ids("PROJ-12 and #287, not #333333, #fdf1e2 or #1")
        self.assertEqual(found, ["PROJ-12", "#287"])

    def test_ticket_ids_skip_common_tech_tokens(self):
        self.assertEqual(st.ticket_ids("fix the UTF-8 decode, SHA-256, GPT-4, PEP-8 and C#12"), [])

    def test_sanitize_keeps_hash_ids_and_strips_markdown_and_quotes(self):
        self.assertEqual(st.sanitize("#287 align screens"), "#287 align screens")
        self.assertEqual(st.sanitize("## Fix sheet"), "Fix sheet")
        self.assertEqual(st.sanitize('"PROJ-1 thing."'), "PROJ-1 thing")
        self.assertEqual(st.sanitize("one two three four five six seven eight nine"), "")

    def test_sanitize_rejects_model_chatter_and_control_characters(self):
        self.assertEqual(st.sanitize("Here is a good title:"), "")
        self.assertEqual(st.sanitize("Sorry, I can't name this"), "")
        self.assertEqual(st.sanitize("I18n setup\x07"), "I18n setup")

    def test_restore_ids_undoes_an_invented_prefix(self):
        self.assertEqual(st.restore_ids("PROJ-253 web form", "fix form #253"), "#253 web form")
        self.assertEqual(st.restore_ids("PROJ-156 sheet", "PROJ-156 sheet bug"), "PROJ-156 sheet")

    def test_clean_prompt_drops_reminders_and_keeps_the_summary_intent(self):
        self.assertEqual(st.clean_prompt("fix <system-reminder>noise</system-reminder> it"), "fix it")
        continued = (
            "This session is being continued...\n1. Primary Request and Intent:\n   Fix the cache\n"
            "   1. sub item\n2. Key Technical Concepts:\n   x"
        )
        self.assertEqual(st.clean_prompt(continued), "Fix the cache 1. sub item")

    def test_compact_summary_reads_the_last_summary_current_work_and_next_step(self):
        old = {"isCompactSummary": True, "message": {"content": "8. Current Work:\n old work"}}
        new = {"isCompactSummary": True, "message": {"content": [
            {"type": "text", "text": "1. Primary Request and Intent:\n x\n8. Current Work:\n Fixing the cache\n"
                                     "9. Optional Next Step:\n ship it"},
        ]}}
        with tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False) as transcript:
            transcript.write(json.dumps(old) + "\n" + json.dumps({"type": "user"}) + "\n" + json.dumps(new) + "\n")
        self.addCleanup(os.unlink, transcript.name)
        self.assertEqual(st.compact_summary(transcript.name), "Fixing the cache ship it")


class HookTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.config = Path(tmp.name)
        (self.config / "sessions").mkdir()
        env = mock.patch.dict(os.environ, {"CLAUDE_CONFIG_DIR": tmp.name})
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop(st.GUARD_ENV, None)

    def session_file(self, name, source):
        info = {"pid": 42, "sessionId": "s1", "name": name, "nameSource": source}
        (self.config / "sessions" / "42.json").write_text(json.dumps(info))

    @staticmethod
    def prompt(text, title=None):
        payload = {"session_id": "s1", "hook_event_name": "UserPromptSubmit", "prompt": text, "cwd": "/w/app"}
        return {**payload, "session_title": title} if title else payload

    def test_names_a_new_session(self):
        out, _ = run_hook(self.prompt("fix the login timeout"))
        self.assertEqual(printed_title(out), "PROJ-1 new name")

    def test_leaves_a_hand_renamed_session_alone_for_good(self):
        self.session_file("My own name", "user")
        out, ask = run_hook(self.prompt("PROJ-9 something new", title="My own name"))
        self.assertEqual((out, ask.call_count), ("", 0))
        self.session_file("My own name", "derived")
        out, ask = run_hook(self.prompt("PROJ-10 more", title="My own name"))
        self.assertEqual((out, ask.call_count), ("", 0))

    def test_replaces_claude_code_derived_placeholder_name(self):
        self.session_file("app-e1", "derived")
        out, _ = run_hook(self.prompt("fix the login timeout", title="app-e1"))
        self.assertEqual(printed_title(out), "PROJ-1 new name")

    def test_after_the_early_prompts_only_a_new_ticket_renames(self):
        for i in range(st.EARLY_PROMPTS):
            run_hook(self.prompt(f"step {i}"), title=f"Name {i}")
        _, ask = run_hook(self.prompt("more of the same work"))
        self.assertEqual(ask.call_count, 0)
        out, ask = run_hook(self.prompt("now PROJ-7 please"), title="PROJ-7 cache fix")
        self.assertEqual((ask.call_count, printed_title(out)), (1, "PROJ-7 cache fix"))

    def test_each_ticket_id_renames_only_once(self):
        for i in range(st.EARLY_PROMPTS):
            run_hook(self.prompt(f"step {i}"), title=f"Name {i}")
        _, ask = run_hook(self.prompt("now PROJ-7 please"), title="Cache fix")
        self.assertEqual(ask.call_count, 1)
        _, ask = run_hook(self.prompt("PROJ-7 still failing", title="Cache fix"))
        self.assertEqual(ask.call_count, 0)

    def test_without_session_files_a_name_the_hook_never_gave_does_not_lock(self):
        out, _ = run_hook(self.prompt("fix the login timeout", title="app-e1"), title="Login timeout")
        self.assertEqual(printed_title(out), "Login timeout")
        out, ask = run_hook(self.prompt("PROJ-3 next", title="My own name"))
        self.assertEqual((out, ask.call_count), ("", 0))

    def test_compaction_still_renames_after_the_prompt_budget_is_spent(self):
        state_dir = self.config / "session-titles"
        state_dir.mkdir()
        (state_dir / "s1.json").write_text(json.dumps({"titles": [], "runs": st.MAX_RUNS, "locked": False}))
        with tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False) as transcript:
            summary = {"isCompactSummary": True, "message": {"content": "8. Current Work:\n Fixing the cache"}}
            transcript.write(json.dumps(summary) + "\n")
        self.addCleanup(os.unlink, transcript.name)
        payload = {"session_id": "s1", "hook_event_name": "SessionStart", "source": "compact",
                   "transcript_path": transcript.name}
        _, ask = run_hook(payload, title="Fix the cache")
        self.assertEqual(ask.call_count, 1)

    def test_compaction_renames_from_the_summary(self):
        with tempfile.NamedTemporaryFile("w", suffix=".jsonl", delete=False) as transcript:
            summary = {"isCompactSummary": True, "message": {"content": "8. Current Work:\n Fixing the cache"}}
            transcript.write(json.dumps(summary) + "\n")
        self.addCleanup(os.unlink, transcript.name)
        payload = {"session_id": "s1", "hook_event_name": "SessionStart", "source": "compact",
                   "transcript_path": transcript.name}
        out, ask = run_hook(payload, title="Fix the cache")
        self.assertIn("Fixing the cache", ask.call_args.args[2])
        self.assertEqual(json.loads(out)["hookSpecificOutput"]["hookEventName"], "SessionStart")

    def test_ignores_a_session_start_that_is_not_a_compaction(self):
        payload = {"session_id": "s1", "hook_event_name": "SessionStart", "source": "startup"}
        out, ask = run_hook(payload)
        self.assertEqual((out, ask.call_count), ("", 0))


if __name__ == "__main__":
    unittest.main()
