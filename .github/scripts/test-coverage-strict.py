#!/usr/bin/env python3
"""Synthetic strict-gate tests: no Go build, tests or measurement."""

import contextlib
import importlib.util
import io
import json
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("coverage_strict", Path(__file__).with_name("coverage-strict.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class StrictCoverageTests(unittest.TestCase):
    def invoke(self, text, scope="full-module", report=None, head=None):
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory) / "profile.cov"
            if text is not None:
                profile.write_text(text, encoding="utf-8")
            argv = [str(profile), "--scope", scope]
            if report is not None:
                ratchet = Path(directory) / "ratchet.json"
                ratchet.write_text(json.dumps(report), encoding="utf-8")
                argv += ["--ratchet", str(ratchet)]
            if head is not None:
                argv += ["--head", head]
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                code = gate.main(argv)
            return code, json.loads(output.getvalue())

    def test_exact_integer_coverage_and_modes(self):
        for mode in ("set", "count", "atomic"):
            for body, want in (("a.go:1.1,2.2 1 1", 0),
                               ("a.go:1.1,2.2 1 0", 1),
                               ("a.go:1.1,2.2 999999 1\na.go:3.1,4.2 1 0", 1),
                               ("a.go:1.1,2.2 0 0\na.go:3.1,4.2 1 1", 0),
                               ("a.go:1.1,2.2 1 0\na.go:1.1,2.2 1 1", 0)):
                with self.subTest(mode=mode, body=body):
                    code, result = self.invoke(f"mode: {mode}\n{body}\n")
                    self.assertEqual(code, want)
                    self.assertEqual(result["status"], "failed" if want else "passed")
                    if "999999" in body:
                        self.assertEqual(result["uncovered"], 1)
                        self.assertEqual(result["uncovered_blocks"], 1)

    def test_missing_empty_and_malformed_fail_closed(self):
        for text in (None, "", "mode: set\n", "mode: set\na.go:1.1,2.2 0 0\n",
                     "mode: invalid\na.go:1.1,2.2 1 1\n", "a.go:1.1,2.2 1 1\n",
                     "mode: set\na.go:1.1,2.2 -1 1\n", "mode: set\na.go:1.1,2.2 1 -1\n",
                     "mode: set\na.go:0.1,2.2 1 1\n", "mode: set\na.go:2.2,1.1 1 1\n",
                     "mode: set\na.go:1.1,2.2 1 1\na.go:1.1,2.2 2 1\n", "mode: set\ngarbage\n"):
            with self.subTest(text=text):
                self.assertEqual(self.invoke(text)[0], 1)

    def test_selected_no_work_requires_current_producer_result(self):
        head, base = "a" * 40, "b" * 40
        report = {"packages": None, "merge_base": base, "scope_identity": {
            "head_sha": head, "base_sha": base, "include_e2e": True, "build_sha256": "c" * 64}}
        code, result = self.invoke("mode: set\n", "selected", report, head)
        self.assertEqual((code, result["status"], result["statements"]), (0, "no-work", 0))
        self.assertEqual(self.invoke("mode: set\n", "full-module", report, head)[0], 1)
        for bad, expected in ((None, head), ({}, head), (report, "d" * 40),
                              ({**report, "packages": [{"package": "example/a"}]}, head),
                              ({**report, "scope_identity": {"head_sha": head}}, head)):
            with self.subTest(report=bad, expected=expected):
                self.assertEqual(self.invoke("mode: set\n", "selected", bad, expected)[0], 1)
        self.assertEqual(self.invoke("", "selected", report, head)[0], 1)
        self.assertEqual(self.invoke("mode: set\na.go:1.1,2.2 0 0\n", "selected", report, head)[0], 1)
        self.assertEqual(self.invoke("mode: set\na.go:1.1,2.2 1 0\n", "selected", report, head)[0], 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
