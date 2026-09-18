#!/usr/bin/env python3
"""Unit tests for extract.py, using tiny synthetic fixtures only.

No real transcript data is read here -- every JSONL line is fabricated in
this file. Run with:

    python3 -m unittest tools/sdlc-metrics/test_extract.py -v

(or `python3 -m unittest` from this directory).
"""
import atexit
import json
import os
import shutil
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import extract  # noqa: E402

# Keep tests hermetic: never let extract.py's /tmp task-output scan pick up
# real transcripts from this machine. Point it at an empty directory.
# Minor #11: this mkdtemp is not a `with` block (it must outlive every test
# in this module), so its cleanup is registered explicitly instead of being
# left to leak on disk after the run.
_EMPTY_TMP_ROOT = tempfile.mkdtemp(prefix="sdlc-metrics-test-tmp-")
os.environ["SDLC_METRICS_TMP_ROOT"] = _EMPTY_TMP_ROOT
atexit.register(shutil.rmtree, _EMPTY_TMP_ROOT, ignore_errors=True)


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


class TestIterationsDedup(unittest.TestCase):
    """B1: usage.iterations, when present, is the full per-sub-request
    breakdown the top-level usage already aggregates. Summing both
    double-counts; only the iterations may be summed when present."""

    def test_iterations_summed_not_added_to_top_level(self):
        bucket = extract.UsageBucket()
        # top-level usage equals the single iteration's usage, as real
        # records do (14,657 of 19,143 in the brief's sample) -- summing
        # both would double it to 200/100.
        bucket.add({
            "input_tokens": 100, "output_tokens": 50,
            "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
            "iterations": [{"type": "message", "model": "claude-sonnet-5",
                             "input_tokens": 100, "output_tokens": 50,
                             "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}],
        }, model="claude-sonnet-5")
        self.assertEqual(bucket.input_tokens, 100)
        self.assertEqual(bucket.output_tokens, 50)

    def test_multiple_iterations_summed(self):
        bucket = extract.UsageBucket()
        bucket.add({
            "input_tokens": 999, "output_tokens": 999,  # must be ignored -- iterations present
            "iterations": [
                {"type": "message", "model": "claude-sonnet-5", "input_tokens": 30, "output_tokens": 10},
                {"type": "message", "model": "claude-haiku-4-5", "input_tokens": 20, "output_tokens": 5},
            ],
        }, model="claude-sonnet-5")
        self.assertEqual(bucket.input_tokens, 50)
        self.assertEqual(bucket.output_tokens, 15)

    def test_empty_iterations_falls_back_to_top_level(self):
        bucket = extract.UsageBucket()
        bucket.add({"input_tokens": 40, "output_tokens": 20, "iterations": []}, model="claude-sonnet-5")
        self.assertEqual(bucket.input_tokens, 40)
        self.assertEqual(bucket.output_tokens, 20)


class TestRequestIdLastMaxDedup(unittest.TestCase):
    """B2: streamed lines sharing a requestId must be merged by per-field
    max (or last), never by keeping the FIRST partial line -- the brief
    found output undercounted by ~59% from taking the first line."""

    def test_differing_streamed_lines_take_max_not_first(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            proj = home / ".claude" / "projects" / "-fake-project"
            proj.mkdir(parents=True)
            session_path = proj / "session-stream.jsonl"
            # three streamed lines for the SAME response, growing monotonically
            # (the shape real transcripts take: each streamed line repeats the
            # cumulative usage seen so far).
            records = []
            for out_tok in (20, 55, 120):  # final line has the full output count
                rec = assistant_msg(
                    "2026-09-12T10:00:00Z", "claude-sonnet-5",
                    {"input_tokens": 200, "output_tokens": out_tok,
                     "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                    [{"type": "text", "text": "partial"}],
                )
                rec["requestId"] = "req-stream-1"
                records.append(rec)
            write_jsonl(session_path, records)

            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            metrics = extract.run_extract(
                since, until, home / "out", home,
                repo="sneat-dev/wb", git_repo_path=home / "no-repo",
                wb_state_dirs=[home / "no-wb"], offline=True,
            )
            by_model = metrics["tokens"]["main_loop_by_model"]
            # must be the LAST/MAX (120), never the first (20) and never
            # summed across lines (20+55+120=195)
            self.assertEqual(by_model["sonnet"]["output_tokens"], 120)
            self.assertEqual(by_model["sonnet"]["input_tokens"], 200)


class TestPriceTablePrefixMatching(unittest.TestCase):
    """B3: prices are keyed by model-id prefix, most specific first, with
    a family fallback, and an explicit legacy fallback for old generations."""

    def test_specific_prefixes_beat_family_fallback(self):
        r_opus5 = extract.price_rates_for_model("claude-opus-5-20260101")
        self.assertAlmostEqual(r_opus5["input"], 5.00)
        self.assertAlmostEqual(r_opus5["output"], 25.00)

        r_sonnet5 = extract.price_rates_for_model("claude-sonnet-5")
        self.assertAlmostEqual(r_sonnet5["input"], 2.00)
        self.assertAlmostEqual(r_sonnet5["output"], 10.00)

        r_haiku = extract.price_rates_for_model("claude-haiku-4-5-20251001")
        self.assertAlmostEqual(r_haiku["input"], 1.00)
        self.assertAlmostEqual(r_haiku["output"], 5.00)

    def test_fable_5_1_prefix_beats_fable_5(self):
        r_5_1 = extract.price_rates_for_model("claude-fable-5-1")
        r_5 = extract.price_rates_for_model("claude-fable-5")
        self.assertAlmostEqual(r_5_1["input"], 10.00)
        self.assertAlmostEqual(r_5_1["output"], 50.00)
        # Fable 5.1's cache read is a flat $0.25/MTok, NOT the standard 0.1x
        self.assertAlmostEqual(r_5_1["cache_read"], 0.25)
        self.assertAlmostEqual(r_5["cache_read"], 1.00)  # 0.1x of $10 input

    def test_legacy_generation_fallback(self):
        r_sonnet4 = extract.price_rates_for_model("claude-sonnet-4-20250101")
        self.assertAlmostEqual(r_sonnet4["input"], 3.00)
        self.assertAlmostEqual(r_sonnet4["output"], 15.00)
        self.assertIn("claude-sonnet-4", extract.LEGACY_PRICE_PREFIXES)
        r_opus4 = extract.price_rates_for_model("claude-opus-4-20250101")
        self.assertAlmostEqual(r_opus4["input"], 15.00)
        self.assertAlmostEqual(r_opus4["output"], 75.00)
        self.assertIn("claude-opus-4", extract.LEGACY_PRICE_PREFIXES)

    def test_unknown_model_falls_back_to_family(self):
        r = extract.price_rates_for_model("claude-sonnet-9000-preview")
        self.assertAlmostEqual(r["input"], extract.PRICE_TABLE_USD_PER_MTOK["sonnet"]["input"])

    def test_prices_as_of_date_recorded_in_metrics(self):
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
            found = json.dumps(metrics)
            self.assertIn(extract.PRICE_TABLE_AS_OF, found)


class TestCommandSanitizerAdversarial(unittest.TestCase):
    """B4: quoted-text words must never leak into verb classification.
    Covers the two real leaks the review found plus two adversarial
    injections, and a full metrics.json/SUMMARY.md scan."""

    LEAK_COMMANDS = [
        # the two real leaks from the review
        '''BODY='## Problem statement here, discussing internals' && gh issue create --title "x" --body "$BODY"''',
        '''C='Closed as not needed' && gh issue close 5 --comment "$C"''',
        # adversarial injections
        'MSG="launch the secret project" git commit -m "$MSG"',
        'DEBUG="a b c" secretword --flag',
    ]
    # "project" is deliberately excluded: it is a legitimate substring of
    # unrelated, non-leaked metric text (e.g. "~/projects/.wb/worklogs"),
    # so it would be a false positive here, not a real B4 leak.
    LEAK_WORDS = ["Problem", "internals", "Closed", "needed", "launch", "secret", "secretword"]

    def test_no_leak_words_in_classified_verb(self):
        for cmd in self.LEAK_COMMANDS:
            verb = extract.classify_bash_verb(cmd)
            for word in self.LEAK_WORDS:
                self.assertNotIn(word, verb, msg=f"leaked {word!r} from {cmd!r} into verb {verb!r}")

    def test_unknown_command_becomes_other_not_named(self):
        verb = extract.classify_bash_verb('DEBUG="a b c" secretword --flag')
        self.assertEqual(verb, "<other>")

    def test_malformed_quoting_yields_unparsed_not_a_crash(self):
        # an unbalanced quote must never raise, and must never leak the
        # dangling text -- fall back to <unparsed>.
        verb = extract.classify_bash_verb("git commit -m 'unterminated")
        self.assertIsInstance(verb, str)

    def test_no_leak_words_anywhere_in_metrics_or_summary(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            proj = home / ".claude" / "projects" / "-fake-project"
            proj.mkdir(parents=True)
            session_path = proj / "session-leaks.jsonl"
            records = []
            ts_base = datetime(2026, 9, 12, 10, 0, 0, tzinfo=timezone.utc)
            for i, cmd in enumerate(self.LEAK_COMMANDS):
                ts = (ts_base.replace(minute=i)).isoformat().replace("+00:00", "Z")
                rec = assistant_msg(
                    ts, "claude-sonnet-5",
                    {"input_tokens": 10, "output_tokens": 5,
                     "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                    [{"type": "tool_use", "id": f"tool-{i}", "name": "Bash", "input": {"command": cmd}}],
                )
                records.append(rec)
                records.append(user_tool_result(ts, f"tool-{i}", "ok"))
            write_jsonl(session_path, records)

            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            out_dir = home / "out"
            metrics = extract.run_extract(
                since, until, out_dir, home,
                repo="sneat-dev/wb", git_repo_path=home / "no-repo",
                wb_state_dirs=[home / "no-wb"], offline=True,
            )
            metrics_text = json.dumps(metrics, indent=2, sort_keys=True)
            summary_text = extract.render_summary(metrics, since, until)
            for word in self.LEAK_WORDS:
                self.assertNotIn(word, metrics_text, msg=f"{word!r} leaked into metrics.json")
                self.assertNotIn(word, summary_text, msg=f"{word!r} leaked into SUMMARY.md")


class TestStallThreeWayClassification(unittest.TestCase):
    """M1: gaps must classify as tool_running / idle_until_resume /
    waiting_notification, across all record types, not just
    assistant-to-assistant gaps."""

    def test_idle_until_resume_via_sendmessage_style_user_turn(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-idle0000000000.jsonl"
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "done with my turn"}]),
                # 10 minutes later, a plain user text turn resumes it (no
                # notification wrapper, no pending tool) -- idle_until_resume
                {"type": "user", "isSidechain": False, "timestamp": "2026-09-12T10:10:05Z",
                 "message": {"role": "user", "content": [{"type": "text", "text": "continue please"}]}},
            ]
            write_jsonl(agent_path, records)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertEqual(a.stalls_by_kind.get("idle_until_resume", 0), 1)
            self.assertEqual(a.stalls_by_kind.get("waiting_notification", 0), 0)
            self.assertEqual(a.stalls_by_kind.get("tool_running", 0), 0)

    def test_tool_running_via_tool_result_closing_pending_tool(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-tool00000000000.jsonl"
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "go test ./..."}}]),
                # 10 minutes later a tool_result closes the long-running call
                user_tool_result("2026-09-12T10:10:05Z", "t1", "PASS"),
            ]
            write_jsonl(agent_path, records)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertEqual(a.stalls_by_kind.get("tool_running", 0), 1)

    def test_waiting_notification_via_task_notification_wrapper(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-notif0000000000.jsonl"
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-sonnet-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "dispatched, moving on"}]),
                {"type": "user", "isSidechain": False, "timestamp": "2026-09-12T10:10:05Z",
                 "message": {"role": "user", "content": [
                     {"type": "text", "text": "<task-notification>\n<task-id>abc</task-id>\ndone\n</task-notification>"}
                 ]}},
            ]
            write_jsonl(agent_path, records)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertEqual(a.stalls_by_kind.get("waiting_notification", 0), 1)


class TestCdPrefixSegmentClassification(unittest.TestCase):
    """M2: a leading `cd X &&` (or timeout/env/nice/VAR=) must not hide the
    real command from verb, CPU-heavy, or adoption classification -- every
    `&&`/`||`/`;`/`|`-separated segment is classified on its own."""

    def test_cd_prefix_does_not_become_other_cd(self):
        segments = list(extract.iter_command_segments("cd /some/worktree && go test ./..."))
        verbs = [seg[0] for seg in segments]
        self.assertNotIn("cd", verbs)
        self.assertTrue(any(v == "go" for v in verbs))

    def test_cpu_heavy_detected_after_cd_prefix(self):
        self.assertTrue(extract.is_cpu_heavy("cd /some/worktree && go test ./..."))

    def test_wb_run_wrapping_scoped_to_its_own_segment(self):
        clauses = extract.cpu_heavy_clauses("cd wt && wb run -- go test ./... && go build ./...")
        wrapped_flags = {text: wrapped for text, wrapped in clauses}
        # the go test clause, wrapped by wb run --, is wrapped=True
        self.assertTrue(any(w for t, w in clauses if "go test" in t))
        # the go build clause, NOT prefixed by wb run, is wrapped=False
        self.assertTrue(any((not w) for t, w in clauses if "go build" in t))

    def test_env_and_timeout_prefixes_stripped(self):
        segments = list(extract.iter_command_segments("timeout 30 env FOO=bar go test ./..."))
        verbs = [seg[0] for seg in segments]
        self.assertIn("go", verbs)
        self.assertNotIn("timeout", verbs)
        self.assertNotIn("env", verbs)


class TestInheritModelPricing(unittest.TestCase):
    """M3: a subagent whose meta.json declares model:"inherit" must be
    bucketed and priced by each message's own `message.model`, never
    lumped into "other" and priced at the Sonnet fallback."""

    def test_inherit_meta_model_uses_message_model_for_pricing_and_label(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-inherit000000.jsonl"
            (agent_dir / "agent-inherit000000.meta.json").write_text(
                json.dumps({"model": "inherit", "toolUseId": "tu1"}), encoding="utf-8")
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-opus-5",
                               {"input_tokens": 100, "output_tokens": 50,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "working"}]),
            ]
            write_jsonl(agent_path, records)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertIsNotNone(a)
            # the label is the OBSERVED model, not the literal "inherit"
            self.assertEqual(a.model, "claude-opus-5")
            # priced at opus rates, not the sonnet "other"-family fallback
            expected = 100 * extract.PRICE_TABLE_USD_PER_MTOK["opus"]["input"] / 1_000_000.0 + \
                50 * extract.PRICE_TABLE_USD_PER_MTOK["opus"]["output"] / 1_000_000.0
            self.assertAlmostEqual(a.usage.cost_usd(), expected, places=6)

    def test_declared_non_inherit_model_used_as_label(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td)
            agent_dir = home / ".claude" / "projects" / "-fake-project" / "sess1" / "subagents"
            agent_dir.mkdir(parents=True)
            agent_path = agent_dir / "agent-declared000000.jsonl"
            (agent_dir / "agent-declared000000.meta.json").write_text(
                json.dumps({"model": "claude-haiku-4-5", "toolUseId": "tu2"}), encoding="utf-8")
            records = [
                assistant_msg("2026-09-12T10:00:00Z", "claude-haiku-4-5",
                               {"input_tokens": 10, "output_tokens": 5,
                                "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
                               [{"type": "text", "text": "working"}]),
            ]
            write_jsonl(agent_path, records)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            a = extract.process_agent_transcript(str(agent_path), since, until, "subagents_dir")
            self.assertEqual(a.model, "claude-haiku-4-5")


class TestPrivacyNoPersonalData(unittest.TestCase):
    """M4: no email addresses and no /home/<user>/... paths may reach
    metrics.json or SUMMARY.md."""

    EMAIL_RE = "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"

    def test_scrub_home_path_replaces_home_with_tilde(self):
        scrubbed = extract.scrub_home_path("/home/someuser/projects/secret-repo/x.py")
        self.assertNotIn("/home/", scrubbed)
        self.assertNotIn("someuser", scrubbed)

    def test_sanitize_pr_fields_drops_body(self):
        pr = {"number": 42, "body": "some PR body with maybe an email a@b.com",
              "created_at": "2026-09-12T00:00:00Z", "merged_at": None,
              "closed_at": None, "state": "open"}
        sanitized = extract._sanitize_pr_fields(pr)
        self.assertNotIn("body", sanitized)
        dumped = json.dumps(sanitized)
        self.assertNotIn("a@b.com", dumped)

    def test_sanitize_run_fields_drops_author_email(self):
        run = {"id": 1, "name": "CI", "event": "push", "conclusion": "success",
               "run_started_at": "2026-09-12T00:00:00Z", "updated_at": "2026-09-12T00:05:00Z",
               "head_sha": "abc123",
               "head_commit": {"author": {"email": "real.person@example.com", "name": "Real Person"}}}
        sanitized = extract._sanitize_run_fields(run)
        dumped = json.dumps(sanitized)
        self.assertNotIn("real.person@example.com", dumped)
        self.assertNotIn("Real Person", dumped)

    def test_no_home_or_email_in_metrics_or_summary(self):
        with tempfile.TemporaryDirectory() as td:
            home = Path(td) / "home" / "fakeuser"
            (home / ".claude" / "projects").mkdir(parents=True)
            since = datetime(2026, 9, 11, tzinfo=timezone.utc)
            until = datetime(2026, 9, 18, 23, 59, 59, tzinfo=timezone.utc)
            # force an unavailable-with-path-in-reason branch by pointing at
            # a real, nonexistent-but-home-rooted git repo path
            metrics = extract.run_extract(
                since, until, home / "out", home,
                repo="sneat-dev/wb", git_repo_path=home / "projects" / "sneat-dev" / "wb",
                wb_state_dirs=[home / ".wb", home / "projects" / ".wb"], offline=True,
            )
            metrics_text = json.dumps(metrics, indent=2, sort_keys=True)
            summary_text = extract.render_summary(metrics, since, until)
            import re as _re
            self.assertNotIn("/home/", metrics_text)
            self.assertNotIn("fakeuser", metrics_text)
            self.assertIsNone(_re.search(self.EMAIL_RE, metrics_text))
            self.assertNotIn("/home/", summary_text)
            self.assertIsNone(_re.search(self.EMAIL_RE, summary_text))


class TestOutRefusesInsideGitRepo(unittest.TestCase):
    """Minor 10: --out must be refused if it resolves inside a git repo."""

    def test_out_inside_git_repo_detected(self):
        with tempfile.TemporaryDirectory() as td:
            repo_root = Path(td) / "repo"
            (repo_root / ".git").mkdir(parents=True)
            out_dir = repo_root / "tools" / "sdlc-metrics" / "scratch-out"
            out_dir.mkdir(parents=True)
            self.assertTrue(extract.path_is_inside_git_repo(out_dir))

    def test_out_outside_git_repo_not_flagged(self):
        with tempfile.TemporaryDirectory() as td:
            out_dir = Path(td) / "scratch-out"
            out_dir.mkdir(parents=True)
            self.assertFalse(extract.path_is_inside_git_repo(out_dir))


if __name__ == "__main__":
    unittest.main()
