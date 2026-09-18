#!/usr/bin/env python3
"""Unit tests for extract.py, using tiny synthetic fixtures only.

No real transcript data is read here -- every JSONL line is fabricated in
this file. Run with:

    python3 -m unittest tools/sdlc-metrics/test_extract.py -v

(or `python3 -m unittest` from this directory).
"""
import json
import os
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import extract  # noqa: E402

# Keep tests hermetic: never let extract.py's /tmp task-output scan pick up
# real transcripts from this machine. Point it at an empty directory.
_EMPTY_TMP_ROOT = tempfile.mkdtemp(prefix="sdlc-metrics-test-tmp-")
os.environ["SDLC_METRICS_TMP_ROOT"] = _EMPTY_TMP_ROOT


def write_jsonl(path: Path, records: list) -> None:
    with open(path, "w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")


def assistant_msg(ts: str, model: str, usage: dict, content: list, is_sidechain: bool = False) -> dict:
    return {
        "type": "assistant",
        "isSidechain": is_sidechain,
        "timestamp": ts,
        "message": {"model": model, "usage": usage, "content": content},
    }


def user_tool_result(ts: str, tool_use_id: str, content, is_error: bool = False, is_sidechain: bool = False) -> dict:
    return {
        "type": "user",
        "isSidechain": is_sidechain,
        "timestamp": ts,
        "message": {
            "role": "user",
            "content": [
                {"type": "tool_result", "tool_use_id": tool_use_id, "content": content, "is_error": is_error}
            ],
        },
    }


class TestTokenAggregation(unittest.TestCase):
    def test_tokens_split_by_model_family(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            proj = home / ".claude" / "projects" / "-fake-project"
            proj.mkdir(parents=True)
            session_path = proj / "session-a.jsonl"
            records = [
                assistant_msg(
                    "2026-09-12T10:00:00Z", "claude-sonnet-5",
                    {"input_tokens": 100, "output_tokens": 50,
                     "cache_creation_input_tokens": 10, "cache_read_input_tokens": 5},
                    [{"type": "text", "text": "hi"}],
                ),
                assistant_msg(
                    "2026-09-12T10:05:00Z", "claude-haiku-4-5-20251001",
                    {"input_tokens": 20, "output_tokens": 8,
                     "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                    [{"type": "text", "text": "hi again"}],
                ),
            ]
            write_jsonl(session_path, records)

            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            out_dir = home / "out"
            wb_state_dir = home / "no-wb"
            git_repo = home / "no-repo"

            metrics = extract.run_extract(
                since, until, out_dir, home,
                repo="sneat-dev/wb", git_repo_path=git_repo, wb_state_dirs=[wb_state_dir],
                offline=True,
            )
            by_model = metrics["tokens"]["main_loop_by_model"]
            self.assertIn("sonnet", by_model)
            self.assertIn("haiku", by_model)
            self.assertEqual(by_model["sonnet"]["input_tokens"], 100)
            self.assertEqual(by_model["haiku"]["input_tokens"], 20)
            # estimated_total_usd is rounded to 2dp and these are tiny token
            # counts, so assert on the per-model 4dp figure instead.
            self.assertGreater(by_model["sonnet"]["estimated_usd"], 0)
            self.assertGreater(by_model["haiku"]["estimated_usd"], 0)

    def test_model_family_matching(self):
        self.assertEqual(extract.model_family("claude-opus-4-8"), "opus")
        self.assertEqual(extract.model_family("claude-sonnet-5"), "sonnet")
        self.assertEqual(extract.model_family("claude-haiku-4-5-20251001"), "haiku")
        self.assertEqual(extract.model_family("claude-fable-5-1"), "fable")
        self.assertEqual(extract.model_family(None), "unknown")
        self.assertEqual(extract.model_family("some-other-model"), "other")


class TestStallDetection(unittest.TestCase):
    def test_stall_over_five_minutes_flagged(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-deadbeef00000000.jsonl"
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "starting work, waiting for notification"}]),
                # 10 minutes later -> a stall, and the prior turn ended with
                # "waiting for notification"
                assistant_msg("2026-09-12T10:10:01Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "done"}]),
            ]
            write_jsonl(agent_path, records)

            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertIsNotNone(a)
            self.assertEqual(a.stalls, 1)
            self.assertEqual(a.stall_ending_waiting, 1)

    def test_no_stall_under_threshold(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-cafebabe00000000.jsonl"
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "step 1"}]),
                assistant_msg("2026-09-12T10:01:00Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "step 2"}]),
            ]
            write_jsonl(agent_path, records)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertEqual(a.stalls, 0)


class TestPrivacyReduction(unittest.TestCase):
    def test_reduce_command_strips_paths_and_quotes(self):
        cmd = 'git commit -m "some prompt-like sentence with secret info" --author="a@b.com"'
        reduced = extract.reduce_command(cmd)
        self.assertNotIn("secret", reduced)
        self.assertNotIn("a@b.com", reduced)
        self.assertTrue(reduced.startswith("git commit"))

    def test_reduce_command_collapses_paths(self):
        cmd = "cat /home/someone/projects/secretrepo/config/creds.yaml"
        reduced = extract.reduce_command(cmd)
        self.assertNotIn("someone", reduced)
        self.assertNotIn("secretrepo", reduced)
        self.assertIn("<path>", reduced)

    def test_classify_bash_verb(self):
        self.assertEqual(extract.classify_bash_verb("go test ./... -run TestFoo"), "go test -run")
        self.assertEqual(extract.classify_bash_verb("go test ./..."), "go test (full)")
        self.assertEqual(extract.classify_bash_verb("wb run -- go build ./..."), "wb run")
        self.assertEqual(extract.classify_bash_verb("wb pr land --json"), "wb pr land")
        self.assertEqual(extract.classify_bash_verb("gh run list --limit 5"), "gh run list")
        self.assertEqual(extract.classify_bash_verb("git status"), "git")

    def test_input_signature_never_leaks_raw_text(self):
        sig = extract.input_signature("Read", {"file_path": "/home/x/secret.env"})
        self.assertNotIn("secret", sig)
        self.assertEqual(len(sig), 16)  # short hash, not the content

    def test_result_content_len_does_not_store_text(self):
        # the function must only return a length, never the string itself
        n = extract.result_content_len("some very secret file contents\npassword=hunter2")
        self.assertIsInstance(n, int)
        self.assertGreater(n, 0)


class TestUnavailableReporting(unittest.TestCase):
    def test_missing_wb_state_dir_reported_unavailable(self):
        result = extract.process_wb_worklogs([Path("/nonexistent/wb/state/dir")],
                                              datetime(2026, 9, 11, tzinfo=timezone.utc),
                                              datetime(2026, 9, 18, tzinfo=timezone.utc))
        self.assertIn("unavailable", result)

    def test_missing_git_repo_reported_unavailable(self):
        result = extract.process_git_log(Path("/nonexistent/repo/path"),
                                          datetime(2026, 9, 11, tzinfo=timezone.utc),
                                          datetime(2026, 9, 18, tzinfo=timezone.utc))
        self.assertIn("unavailable", result)

    def test_run_extract_lists_unavailable_metrics_with_reasons(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            (home / ".claude" / "projects").mkdir(parents=True)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            metrics = extract.run_extract(
                since, until, home / "out", home,
                repo="sneat-dev/wb", git_repo_path=home / "no-repo",
                wb_state_dirs=[home / "no-wb"], offline=True,
            )
            self.assertIn("unavailable", metrics)
            self.assertGreater(len(metrics["unavailable"]), 0)
            for u in metrics["unavailable"]:
                self.assertIn("metric", u)
                self.assertIn("reason", u)
                self.assertTrue(u["reason"])


class TestBashSubverbAndSequences(unittest.TestCase):
    def test_leading_verb_and_subverb(self):
        self.assertEqual(extract.leading_verb_and_subverb("wb pr land --json")[1], "wb pr land")
        self.assertEqual(extract.leading_verb_and_subverb("git push origin main")[1], "git push")
        self.assertEqual(extract.leading_verb_and_subverb("gh api repos/x/y")[1], "gh api")
        self.assertEqual(extract.leading_verb_and_subverb("go test ./...")[1], "go test")

    def test_cpu_heavy_detection_and_wb_run_wrapping(self):
        self.assertTrue(extract.is_cpu_heavy("go test ./..."))
        self.assertTrue(extract.is_cpu_heavy("golangci-lint run"))
        self.assertFalse(extract.is_cpu_heavy("git status"))
        self.assertTrue(extract.is_wb_run_wrapped("wb run -- go test ./..."))
        self.assertFalse(extract.is_wb_run_wrapped("go test ./..."))

    def test_hand_rolled_loop_detection(self):
        self.assertTrue(extract.is_hand_rolled_loop("until gh pr checks 123; do sleep 5; done"))
        self.assertTrue(extract.is_hand_rolled_loop("while kill -0 $PID; do sleep 1; done"))
        self.assertTrue(extract.is_hand_rolled_loop("gh run list --limit 5"))
        self.assertFalse(extract.is_hand_rolled_loop("git status"))

    def test_bounded_pipe_detection(self):
        self.assertTrue(extract.has_bounded_pipe("git log | head -20"))
        self.assertFalse(extract.has_bounded_pipe("git log | grep foo"))
        self.assertIsNone(extract.has_bounded_pipe("git status"))

    def test_sequence_miner_finds_repeated_ngrams(self):
        miner = extract.SequenceMiner(max_n=3, min_n=2)
        miner.feed_session(["git fetch", "git merge", "go build"])
        miner.feed_session(["git fetch", "git merge", "go build"])
        top = miner.top(5)
        seqs = [tuple(t["sequence"]) for t in top]
        self.assertIn(("git fetch", "git merge"), seqs)
        # a sequence occurring only once anywhere in aggregate should not appear
        miner2 = extract.SequenceMiner(max_n=2, min_n=2)
        miner2.feed_session(["a", "b"])
        self.assertEqual(miner2.top(5), [])


class TestRequestIdDedup(unittest.TestCase):
    """One API response is split across several JSONL lines (one per
    content block, e.g. a thinking block then a tool_use block), each
    repeating the SAME usage object. Usage must be counted once per
    requestId, not once per line."""

    def test_duplicate_usage_lines_counted_once(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            proj = home / ".claude" / "projects" / "-fake-project"
            proj.mkdir(parents=True)
            session_path = proj / "session-dupe.jsonl"
            usage = {"input_tokens": 100, "output_tokens": 50,
                      "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
            # three JSONL lines, same requestId, same usage (as real
            # transcripts do for a multi-block response)
            records = []
            for _ in range(3):
                rec = assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5", usage,
                                     [{"type": "text", "text": "block"}])
                rec["requestId"] = "req-same-response"
                records.append(rec)
            write_jsonl(session_path, records)

            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            out_dir = home / "out"
            metrics = extract.run_extract(
                since, until, out_dir, home,
                repo="sneat-dev/wb", git_repo_path=home / "no-repo",
                wb_state_dirs=[home / "no-wb"], offline=True,
            )
            by_model = metrics["tokens"]["main_loop_by_model"]
            # not 300 -- the duplicate lines must not triple-count
            self.assertEqual(by_model["sonnet"]["input_tokens"], 100)
            self.assertEqual(by_model["sonnet"]["output_tokens"], 50)

    def test_different_request_ids_both_counted(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            proj = home / ".claude" / "projects" / "-fake-project"
            proj.mkdir(parents=True)
            session_path = proj / "session-distinct.jsonl"
            usage = {"input_tokens": 10, "output_tokens": 5,
                      "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
            rec1 = assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5", usage, [{"type": "text", "text": "a"}])
            rec1["requestId"] = "req-1"
            rec2 = assistant_msg("2026-09-12T10:01:00Z", "claude-sonnet-5", usage, [{"type": "text", "text": "b"}])
            rec2["requestId"] = "req-2"
            write_jsonl(session_path, [rec1, rec2])

            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            metrics = extract.run_extract(
                since, until, home / "out", home,
                repo="sneat-dev/wb", git_repo_path=home / "no-repo",
                wb_state_dirs=[home / "no-wb"], offline=True,
            )
            by_model = metrics["tokens"]["main_loop_by_model"]
            self.assertEqual(by_model["sonnet"]["input_tokens"], 20)


class TestCacheWritePricing(unittest.TestCase):
    def test_5m_and_1h_cache_writes_priced_separately(self):
        bucket = extract.UsageBucket()
        bucket.add({
            "input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0,
            "cache_creation": {"ephemeral_5m_input_tokens": 1_000_000, "ephemeral_1h_input_tokens": 0},
        })
        cost_5m_only = bucket.cost_usd("sonnet")

        bucket2 = extract.UsageBucket()
        bucket2.add({
            "input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0,
            "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 1_000_000},
        })
        cost_1h_only = bucket2.cost_usd("sonnet")

        # the two cache-write kinds must be priced differently (1h costs more)
        self.assertNotAlmostEqual(cost_5m_only, cost_1h_only)
        self.assertGreater(cost_1h_only, cost_5m_only)
        self.assertAlmostEqual(cost_5m_only, extract.PRICE_TABLE_USD_PER_MTOK["sonnet"]["cache_write_5m"])
        self.assertAlmostEqual(cost_1h_only, extract.PRICE_TABLE_USD_PER_MTOK["sonnet"]["cache_write_1h"])

    def test_missing_cache_creation_dict_assumed_1h(self):
        # older/synthetic usage objects without a `cache_creation` sub-dict
        # only carry the flat `cache_creation_input_tokens` figure; this
        # harness's orchestrator writes 1h caches, so treat it as 1h.
        bucket = extract.UsageBucket()
        bucket.add({"input_tokens": 0, "output_tokens": 0,
                     "cache_read_input_tokens": 0, "cache_creation_input_tokens": 1_000_000})
        self.assertEqual(bucket.cache_write_1h_tokens, 1_000_000)
        self.assertEqual(bucket.cache_write_5m_tokens, 0)


class TestAdoptionClassifiers(unittest.TestCase):
    def test_symbol_like_patterns(self):
        self.assertEqual(extract.classify_grep_pattern("func DoThing"), "symbol-like")
        self.assertEqual(extract.classify_grep_pattern("type Widget"), "symbol-like")
        self.assertEqual(extract.classify_grep_pattern("HandleRequest("), "symbol-like")
        self.assertEqual(extract.classify_grep_pattern("MyIdentifier"), "symbol-like")

    def test_regex_patterns(self):
        self.assertEqual(extract.classify_grep_pattern(r"foo.*bar\d+"), "regex")
        self.assertEqual(extract.classify_grep_pattern(r"^TODO:"), "regex")

    def test_text_patterns(self):
        self.assertEqual(extract.classify_grep_pattern("please fix this"), "text")
        self.assertEqual(extract.classify_grep_pattern(""), "text")

    def test_spec_path_classes(self):
        self.assertEqual(extract.classify_spec_path("spec/rules/foo/README.md"), "spec/rules")
        self.assertEqual(extract.classify_spec_path("spec/ideas/bar.md"), "spec/ideas")
        self.assertEqual(extract.classify_spec_path("spec/features/baz.md"), "spec/features")
        self.assertEqual(extract.classify_spec_path("spec/decisions/0010.md"), "spec/decisions")
        self.assertEqual(extract.classify_spec_path("spec/lessons/x.md"), "spec/lessons")
        self.assertEqual(extract.classify_spec_path("spec/other-thing.md"), "spec/other")
        self.assertIsNone(extract.classify_spec_path("README.md"))
        self.assertIsNone(extract.classify_spec_path("cmd/wb/main.go"))

    def test_direct_cli_detection_never_stores_raw_command(self):
        agg = extract.AdoptionAgg()
        agg.record_direct_cli("main", "sonnet", "codegrapher", "callers")
        agg.record_direct_cli("main", "sonnet", "specscore", "rule")
        self.assertEqual(agg.direct_cli_calls[("main", "sonnet", "codegrapher", "callers")], 1)
        self.assertEqual(agg.direct_cli_calls[("main", "sonnet", "specscore", "rule")], 1)

    def test_looks_like_source_file(self):
        self.assertTrue(extract.looks_like_source_file("cmd/wb/main.go"))
        self.assertTrue(extract.looks_like_source_file("src/app.tsx"))
        self.assertFalse(extract.looks_like_source_file("README.md"))
        self.assertFalse(extract.looks_like_source_file("data.json"))


if __name__ == "__main__":
    unittest.main()
