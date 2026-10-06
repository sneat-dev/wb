#!/usr/bin/env python3
"""Check exact statement counts in an already measured Go coverage profile."""

import argparse
import json
import re
import sys
from pathlib import Path


BLOCK = re.compile(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)")
SHA = re.compile(r"[0-9a-f]{40}(?:[0-9a-f]{24})?")


def profile_counts(text):
    lines = text.splitlines()
    if not lines or lines[0].strip() not in {"mode: set", "mode: count", "mode: atomic"}:
        raise ValueError("missing or invalid coverage mode")
    blocks = {}
    for number, line in enumerate(lines[1:], 2):
        if not line.strip():
            continue
        match = BLOCK.fullmatch(line.strip())
        if not match:
            raise ValueError(f"malformed coverage block at line {number}")
        file, *numbers = match.groups()
        start_line, start_col, end_line, end_col, statements, count = map(int, numbers)
        if not file.strip() or "\x00" in file or min(start_line, start_col, end_line, end_col) < 1 or (end_line, end_col) < (start_line, start_col):
            raise ValueError(f"invalid coverage location at line {number}")
        key = (file, start_line, start_col, end_line, end_col)
        if key in blocks:
            old_statements, old_count = blocks[key]
            if old_statements != statements:
                raise ValueError(f"conflicting statement count at line {number}")
            # Logical reachability is OR in every Go counter mode.
            count = int(bool(old_count or count))
        blocks[key] = (statements, count)
    total = sum(statements for statements, _ in blocks.values())
    uncovered = sum(statements for statements, count in blocks.values() if count == 0)
    return {"blocks": len(blocks), "statements": total, "covered": total - uncovered, "uncovered": uncovered,
            "positive_blocks": sum(statements > 0 for statements, _ in blocks.values()),
            "uncovered_blocks": sum(statements > 0 and count == 0 for statements, count in blocks.values())}


def selected_no_work(report, head):
    identity = report.get("scope_identity")
    return (SHA.fullmatch(head or "") is not None and isinstance(identity, dict)
            and identity.get("head_sha") == head
            and SHA.fullmatch(identity.get("base_sha", "")) is not None
            and report.get("merge_base") == identity["base_sha"]
            and identity.get("include_e2e") is True
            and re.fullmatch(r"[0-9a-f]{64}", identity.get("build_sha256", "")) is not None
            and "packages" in report and report["packages"] in (None, []))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--scope", choices=("selected", "full-module"), required=True)
    parser.add_argument("--ratchet", type=Path)
    parser.add_argument("--head")
    args = parser.parse_args(argv)
    result = {"scope": args.scope, "status": "failed"}
    try:
        counts = profile_counts(args.profile.read_text(encoding="utf-8"))
        result.update(counts)
        if counts["statements"] == 0:
            if args.scope != "selected" or args.ratchet is None or counts["blocks"] != 0:
                raise ValueError("coverage requires a positive statement denominator")
            report = json.loads(args.ratchet.read_text(encoding="utf-8"))
            if not isinstance(report, dict) or not selected_no_work(report, args.head):
                raise ValueError("empty selected profile lacks a current no-work ratchet result")
            result["status"] = "no-work"
        elif counts["uncovered"] != 0 or counts["uncovered_blocks"] != 0:
            raise ValueError("strict coverage requires zero uncovered positive statement blocks")
        else:
            result["status"] = "passed"
    except (OSError, ValueError, TypeError) as error:
        result["error"] = str(error)
    print(json.dumps(result, sort_keys=True))
    return 0 if result["status"] in {"passed", "no-work"} else 1


if __name__ == "__main__":
    sys.exit(main())
