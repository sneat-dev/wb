#!/usr/bin/env python3
"""SDLC cost and performance metrics extractor.

Turns raw agent-harness transcripts, WB state, and GitHub PR/Actions data
into a small, deterministic metrics pack: metrics.json (machine-readable)
and SUMMARY.md (human-readable). No prompt/response text, file contents,
emails, or secrets are ever written to the pack -- see README.md.

Python 3 standard library only. Streams large files line by line; never
loads a whole transcript into memory.

Usage:
    extract.py --since 2026-09-11 --until 2026-09-18 --out <dir>

The same inputs (same files on disk, same --since/--until) produce the
same output, modulo GitHub state changing between runs (raw API responses
are cached under <out>/raw/ so a later run can reuse them with --offline).
"""
from __future__ import annotations

import argparse
import glob
import hashlib
import json
import os
import re
import statistics
import subprocess
import sys
import time
from collections import Counter, defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable, Iterator, Optional

# --------------------------------------------------------------------------
# Price table (USD per million tokens). EDIT HERE as pricing changes.
# Keyed by a coarse model family; matched by substring against the raw
# model string found in transcripts (e.g. "claude-sonnet-5" -> "sonnet").
# Source: publicly posted list prices at time of writing; re-check before
# trusting absolute dollar figures, they are for relative comparison.
# --------------------------------------------------------------------------
# cache_write_5m / cache_write_1h are separate because the two ephemeral
# cache lifetimes are priced differently (1h costs more to write, both read
# at the same discounted `cache_read` rate). `cache_write` is kept as an
# equal-to-5m fallback for any code path that hasn't been updated to the
# split fields.
PRICE_TABLE_USD_PER_MTOK = {
    "opus":   {"input": 15.00, "output": 75.00, "cache_write": 18.75, "cache_write_5m": 18.75, "cache_write_1h": 30.00, "cache_read": 1.50},
    "sonnet": {"input": 3.00,  "output": 15.00, "cache_write": 3.75,  "cache_write_5m": 3.75,  "cache_write_1h": 6.00,  "cache_read": 0.30},
    "haiku":  {"input": 1.00,  "output": 5.00,  "cache_write": 1.25,  "cache_write_5m": 1.25,  "cache_write_1h": 2.00,  "cache_read": 0.10},
    "fable":  {"input": 1.00,  "output": 5.00,  "cache_write": 1.25,  "cache_write_5m": 1.25,  "cache_write_1h": 2.00,  "cache_read": 0.10},
}
DEFAULT_FAMILY = "sonnet"  # fallback price bucket for an unrecognised model string

# Optional local price-table override, e.g. ~/.config/sdlc-metrics/prices.json
# ({"sonnet": {"input": ..., ...}, ...}) or a path given with --price-config.
# This repository is public: no machine-specific override lives in it, only
# this mechanism to load one from outside it.
_PRICE_CONFIG_ENV = "SDLC_METRICS_PRICE_CONFIG"


def load_price_overrides(path: Optional[Path]) -> None:
    candidates = []
    if path:
        candidates.append(path)
    env_path = os.environ.get(_PRICE_CONFIG_ENV)
    if env_path:
        candidates.append(Path(env_path))
    candidates.append(Path.home() / ".config" / "sdlc-metrics" / "prices.json")
    for p in candidates:
        if p and p.exists():
            try:
                overrides = json.loads(p.read_text(encoding="utf-8"))
            except Exception as e:
                eprint(f"warn: could not read price overrides at {p}: {e}")
                return
            for fam, rates in overrides.items():
                PRICE_TABLE_USD_PER_MTOK.setdefault(fam, {}).update(rates)
            eprint(f"price overrides loaded from {p}")
            return

STALL_THRESHOLD_SECONDS = 5 * 60
CHARS_PER_TOKEN_ESTIMATE = 4.0  # rough, English-text heuristic; see README

CPU_HEAVY_PATTERNS = [
    # NOT anchored to the start of the command: a CPU-heavy call wrapped as
    # `wb run -- go test ./...` has `go test` after the `--`, and that is
    # exactly the case wb_run coverage needs to detect as wrapped.
    re.compile(r"\bgo\s+(test|build|vet)\b"),
    re.compile(r"\bgolangci-lint\b"),
    re.compile(r"\b(npm|pnpm|bun|bunx|yarn)\s+(run\s+)?(build|test)\b"),
]

LOOP_PATTERNS = [
    re.compile(r"^(until|while)\b.*\bsleep\b"),
    re.compile(r"^(until|while)\b"),
    re.compile(r"\bkill\s+-0\b"),
]

POLL_COMMAND_HINTS = re.compile(r"^gh\s+(run\s+list|api\b.*runs|pr\s+checks)\b")

PATH_RE = re.compile(r"(?:/[\w.\-]+){2,}")
LONG_FLAG_VALUE_RE = re.compile(r"(--?[A-Za-z0-9_-]+=)\S+")
QUOTED_RE = [re.compile(r'"[^"]*"'), re.compile(r"'[^']*'")]

# subverb taken as the first non-flag token after the leading verb, for a
# short list of tools whose subcommand matters for this analysis.
SUBVERB_TOOLS = {"wb", "git", "gh", "go", "npm", "pnpm", "bun", "bunx", "yarn"}
CLI_KNOWLEDGE_TOOLS = {"specscore", "codegrapher"}

# --- SpecScore/CodeGrapher adoption classifiers -----------------------------
# These only ever return a short class label. The pattern/path text that was
# classified is discarded immediately after use; it is never written out.

SYMBOL_LIKE_PATTERNS = [
    re.compile(r"^(func|type|class|def|interface)\s+\w+"),
    re.compile(r"^[A-Za-z_][A-Za-z0-9_]*\("),          # a call site: Foo(
    re.compile(r"\.[A-Za-z_][A-Za-z0-9_]*\b"),           # a member access: .Foo
    re.compile(r"^[A-Za-z_][A-Za-z0-9_]{2,}$"),          # a bare identifier
]
REGEX_METACHAR_RE = re.compile(r"[.*+?\[\]{}()|^$\\]")


def classify_grep_pattern(pattern: str) -> str:
    """Classify a grep/rg/Grep-tool pattern as 'symbol-like' (looks like a
    code-structure lookup a CodeGrapher query would answer directly),
    'regex' (a real regular expression, not just an identifier), or 'text'
    (free text search). Returns only the class; the pattern itself must
    never be logged by the caller."""
    p = (pattern or "").strip()
    if not p:
        return "text"
    for pat in SYMBOL_LIKE_PATTERNS:
        if pat.search(p):
            return "symbol-like"
    if REGEX_METACHAR_RE.search(p):
        return "regex"
    return "text"


SPEC_PATH_CLASS_PATTERNS = [
    ("spec/rules", re.compile(r"(^|/)spec/rules(/|$)|(^|/)rules/README\.md$")),
    ("spec/ideas", re.compile(r"(^|/)spec/ideas(/|$)")),
    ("spec/features", re.compile(r"(^|/)spec/features(/|$)")),
    ("spec/decisions", re.compile(r"(^|/)spec/decisions(/|$)")),
    ("spec/lessons", re.compile(r"(^|/)spec/lessons(/|$)")),
    ("spec/other", re.compile(r"(^|/)spec/")),
]


def classify_spec_path(path: str) -> Optional[str]:
    """Classify a file path under a specs tree into a coarse class (never
    return or log the path itself). None if the path is not spec-shaped."""
    p = (path or "")
    for label, pat in SPEC_PATH_CLASS_PATTERNS:
        if pat.search(p):
            return label
    return None


SOURCE_EXT_RE = re.compile(r"\.(go|ts|tsx|js|jsx|py|java|rb|rs|c|cc|cpp|h|hpp|cs|kt|swift)$")


def looks_like_source_file(path: str) -> bool:
    return bool(SOURCE_EXT_RE.search(path or ""))


def extract_bash_grep_pattern(cmd: str) -> Optional[str]:
    """Best-effort extraction of the search pattern from a grep/rg/git-grep
    Bash invocation, for classification only -- never returned to a caller
    that might log it verbatim."""
    m = re.match(r"^(?:git\s+grep|rg|grep)\b(.*)$", cmd.strip())
    if not m:
        return None
    rest = m.group(1)
    # drop flag tokens (-n, -r, --include=..., etc.) to find the first
    # positional argument, which is the pattern in the common invocations
    # this heuristic targets
    toks = rest.split()
    for t in toks:
        if t.startswith("-"):
            continue
        return t.strip("'\"")
    return None


def extract_bash_read_path(cmd: str) -> Optional[str]:
    """Best-effort path argument for cat/head/tail/sed -n <path> reads."""
    m = re.match(r"^(cat|head|tail)\b(.*)$", cmd.strip())
    if m:
        toks = [t for t in m.group(2).split() if not t.startswith("-")]
        return toks[0] if toks else None
    m = re.match(r"^sed\s+-n\b(.*)$", cmd.strip())
    if m:
        toks = [t for t in m.group(1).split() if not t.startswith("-") and not re.match(r"^[\d,$p]+$", t)]
        return toks[-1] if toks else None
    return None


def eprint(*a: Any, **kw: Any) -> None:
    print(*a, file=sys.stderr, **kw)


# --------------------------------------------------------------------------
# Small helpers
# --------------------------------------------------------------------------

def parse_ts(s: Optional[str]) -> Optional[datetime]:
    if not s:
        return None
    try:
        if s.endswith("Z"):
            s = s[:-1] + "+00:00"
        return datetime.fromisoformat(s)
    except Exception:
        return None


def day_key(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%d")


def model_family(model: Optional[str]) -> str:
    if not model:
        return "unknown"
    m = model.lower()
    for fam in ("opus", "sonnet", "haiku", "fable"):
        if fam in m:
            return fam
    return "other"


def in_window(dt: Optional[datetime], since: datetime, until: datetime) -> bool:
    if dt is None:
        return False
    return since <= dt <= until


def reduce_command(cmd: str) -> str:
    """Reduce a shell command line to verb + flags, paths collapsed.

    Privacy: no literal path segments, no quoted string arguments, no other
    free text -- only the leading verb and flag tokens survive.
    """
    cmd = cmd.strip()
    cmd = PATH_RE.sub("<path>", cmd)
    cmd = LONG_FLAG_VALUE_RE.sub(r"\1<val>", cmd)
    for pat in QUOTED_RE:
        cmd = pat.sub("<str>", cmd)
    tokens = cmd.split()
    return " ".join(tokens[:6])  # verb + first few flags only


def command_tokens(cmd: str) -> list:
    return cmd.strip().split()


ENV_ASSIGNMENT_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
SAFE_VERB_RE = re.compile(r"^[A-Za-z][A-Za-z0-9_.\-]*$")


def _sanitize_verb(tok: str) -> str:
    """A verb/subverb token must never carry a path, an assignment's value,
    or anything else that is not a short bare command name -- collapse it
    to a generic placeholder rather than risk leaking a real path."""
    if not tok:
        return "(empty)"
    if ENV_ASSIGNMENT_RE.match(tok):
        return "<assignment>"
    if SAFE_VERB_RE.match(tok):
        return tok
    return "<other>"


def leading_verb_and_subverb(cmd: str) -> tuple:
    """Return (verb, verb_subverb) for a shell command's first pipeline
    segment, e.g. 'wb pr land --json' -> ('wb', 'wb pr land'). Both parts
    are sanitized so a path or an assignment's value can never appear."""
    first_segment = re.split(r"[|;&]", cmd.strip(), maxsplit=1)[0].strip()
    toks = first_segment.split()
    if not toks:
        return "(empty)", "(empty)"
    # skip any number of leading VAR=value assignments (env-prefixed
    # commands, e.g. "GOFLAGS=-p=2 go test ./...") to find the real verb
    lead = 0
    while lead < len(toks) - 1 and ENV_ASSIGNMENT_RE.match(toks[lead]):
        lead += 1
    if lead > 0:
        toks = toks[lead:]
    verb = toks[0]
    if ENV_ASSIGNMENT_RE.match(verb):
        # the whole segment was assignments with no trailing command
        return "<assignment>", "<assignment>"
    # strip a leading sudo/env wrapper, best-effort
    if verb in ("sudo", "env") and len(toks) > 1:
        toks = toks[1:]
        verb = toks[0] if toks else verb
    if verb in SUBVERB_TOOLS and len(toks) > 1:
        sub = _sanitize_verb(toks[1])
        third = _sanitize_verb(toks[2]) if len(toks) > 2 else None
        # go test/build/vet, git push/fetch/merge, gh api/run, wb pr/run/wait...
        if verb == "go" and sub in ("test", "build", "vet", "run", "mod", "vendor"):
            return verb, f"{verb} {sub}"
        if verb in ("git", "gh") and third is not None:
            two_word = f"{sub} {third}"
            if two_word in ("run list", "pr checks", "pr view", "pr create", "pr merge",
                             "issue view", "issue list"):
                return verb, f"{verb} {two_word}"
            return verb, f"{verb} {sub}"
        if verb in ("git", "gh"):
            return verb, f"{verb} {sub}"
        if verb == "wb" and third is not None and toks[1] in ("pr", "worktree", "deps", "agent", "stream", "branch"):
            return verb, f"{verb} {sub} {third}"
        if verb == "wb":
            return verb, f"{verb} {sub}"
        return verb, f"{verb} {sub}"
    return _sanitize_verb(verb), _sanitize_verb(verb)


def classify_bash_verb(cmd: str) -> str:
    """Backward-compatible coarse classification used for the top-level
    tool_patterns.bash_by_verb table."""
    verb, subverb = leading_verb_and_subverb(cmd)
    c = cmd.strip()
    if verb == "go" and "test" in subverb:
        return "go test -run" if "-run" in c else "go test (full)"
    if subverb in ("wb pr land",):
        return "wb pr land"
    if subverb == "wb wait":
        return "wb wait"
    if verb == "wb":
        return "wb run" if subverb == "wb run" else "wb (other)"
    if subverb == "gh run list":
        return "gh run list"
    if verb == "gh":
        return "gh api" if subverb == "gh api" else "gh (other)"
    if any(p.search(c) for p in LOOP_PATTERNS):
        return "sleep/poll-loop"
    if verb == "git":
        return "git"
    return f"other:{verb}"


def is_cpu_heavy(cmd: str) -> bool:
    return any(p.search(cmd.strip()) for p in CPU_HEAVY_PATTERNS)


def is_wb_run_wrapped(cmd: str) -> bool:
    return bool(re.search(r"\bwb\s+run\b", cmd))


def is_hand_rolled_loop(cmd: str) -> bool:
    return any(p.search(cmd.strip()) for p in LOOP_PATTERNS) or bool(POLL_COMMAND_HINTS.search(cmd.strip()))


def has_bounded_pipe(cmd: str) -> Optional[bool]:
    """True if the command pipes through tail/head (bounded output), False
    if it has a pipe but no bound, None if there is no pipe at all."""
    if "|" not in cmd:
        return None
    segments = [s.strip() for s in cmd.split("|")]
    return any(re.match(r"^(tail|head)\b", s) for s in segments[1:])


def input_signature(tool_name: str, tool_input: dict) -> str:
    """A privacy-safe, stable signature for detecting repeated identical
    calls: never the raw input (may contain file contents / prompts), just
    a hash of its JSON shape plus the (command-reduced) Bash command."""
    if tool_name == "Bash":
        cmd = (tool_input or {}).get("command") or ""
        basis = reduce_command(cmd)
    else:
        try:
            basis = json.dumps(tool_input, sort_keys=True, default=str)
        except Exception:
            basis = str(tool_input)
    return hashlib.sha1(basis.encode("utf-8", "replace")).hexdigest()[:16]


# --------------------------------------------------------------------------
# Usage accumulator
# --------------------------------------------------------------------------

@dataclass
class UsageBucket:
    input_tokens: int = 0
    output_tokens: int = 0
    cache_write_5m_tokens: int = 0
    cache_write_1h_tokens: int = 0
    cache_read_tokens: int = 0
    messages: int = 0

    @property
    def cache_write_tokens(self) -> int:
        return self.cache_write_5m_tokens + self.cache_write_1h_tokens

    def add(self, usage: dict) -> None:
        """Add one API response's usage. Cache-write tokens are split into
        the 5-minute and 1-hour ephemeral buckets (different price) when
        `cache_creation` is present; otherwise the whole
        `cache_creation_input_tokens` figure is assumed 1h, since that is
        what this harness's orchestrator writes (see README)."""
        self.input_tokens += int(usage.get("input_tokens") or 0)
        self.output_tokens += int(usage.get("output_tokens") or 0)
        self.cache_read_tokens += int(usage.get("cache_read_input_tokens") or 0)
        cc = usage.get("cache_creation")
        if isinstance(cc, dict):
            self.cache_write_5m_tokens += int(cc.get("ephemeral_5m_input_tokens") or 0)
            self.cache_write_1h_tokens += int(cc.get("ephemeral_1h_input_tokens") or 0)
        else:
            self.cache_write_1h_tokens += int(usage.get("cache_creation_input_tokens") or 0)
        self.messages += 1
        # advisor/server-side usage.iterations, where present, carry their
        # own token counts for sub-requests folded into one message; add
        # them too so a multi-iteration response isn't undercounted.
        for it in usage.get("iterations") or []:
            if not isinstance(it, dict):
                continue
            self.input_tokens += int(it.get("input_tokens") or 0)
            self.output_tokens += int(it.get("output_tokens") or 0)
            self.cache_read_tokens += int(it.get("cache_read_input_tokens") or 0)
            it_cc = it.get("cache_creation")
            if isinstance(it_cc, dict):
                self.cache_write_5m_tokens += int(it_cc.get("ephemeral_5m_input_tokens") or 0)
                self.cache_write_1h_tokens += int(it_cc.get("ephemeral_1h_input_tokens") or 0)

    def cost_usd(self, family: str) -> float:
        p = PRICE_TABLE_USD_PER_MTOK.get(family, PRICE_TABLE_USD_PER_MTOK[DEFAULT_FAMILY])
        return (
            self.input_tokens * p["input"]
            + self.output_tokens * p["output"]
            + self.cache_write_5m_tokens * p.get("cache_write_5m", p.get("cache_write", 0))
            + self.cache_write_1h_tokens * p.get("cache_write_1h", p.get("cache_write", 0))
            + self.cache_read_tokens * p["cache_read"]
        ) / 1_000_000.0

    def as_dict(self, family: Optional[str] = None) -> dict:
        d = {
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cache_write_tokens": self.cache_write_tokens,
            "cache_write_5m_tokens": self.cache_write_5m_tokens,
            "cache_write_1h_tokens": self.cache_write_1h_tokens,
            "cache_read_tokens": self.cache_read_tokens,
            "messages": self.messages,
        }
        if family is not None:
            d["estimated_usd"] = round(self.cost_usd(family), 4)
        return d


def merge_bucket(dst: UsageBucket, src: UsageBucket) -> None:
    dst.input_tokens += src.input_tokens
    dst.output_tokens += src.output_tokens
    dst.cache_write_5m_tokens += src.cache_write_5m_tokens
    dst.cache_write_1h_tokens += src.cache_write_1h_tokens
    dst.cache_read_tokens += src.cache_read_tokens
    dst.messages += src.messages


def _bucket_as_usage_dict(b: "UsageBucket") -> dict:
    """Re-shape an already-aggregated UsageBucket as a single raw `usage`
    object, so it can be fed back into another UsageBucket.add() (used to
    roll a session's or agent's totals into a day/model/role bucket without
    losing the 5m/1h cache-write split)."""
    return {
        "input_tokens": b.input_tokens,
        "output_tokens": b.output_tokens,
        "cache_read_input_tokens": b.cache_read_tokens,
        "cache_creation": {
            "ephemeral_5m_input_tokens": b.cache_write_5m_tokens,
            "ephemeral_1h_input_tokens": b.cache_write_1h_tokens,
        },
    }


# --------------------------------------------------------------------------
# Tool-call statistics accumulator (all tools, not just Bash)
# --------------------------------------------------------------------------

class SizeStats:
    """Streaming holder for a list of sizes; keeps the raw list (small:
    one int per tool call, no text) so percentiles can be computed once
    at the end."""

    __slots__ = ("sizes",)

    def __init__(self):
        self.sizes: list = []

    def add(self, n: int) -> None:
        self.sizes.append(n)

    def summary(self) -> dict:
        if not self.sizes:
            return {"count": 0, "total_chars": 0, "p50_chars": 0, "p95_chars": 0, "max_chars": 0}
        s = sorted(self.sizes)
        n = len(s)

        def pct(p):
            idx = min(n - 1, int(p * (n - 1)))
            return s[idx]

        total = sum(s)
        return {
            "count": n,
            "total_chars": total,
            "p50_chars": pct(0.50),
            "p95_chars": pct(0.95),
            "max_chars": s[-1],
            "estimated_total_tokens": round(total / CHARS_PER_TOKEN_ESTIMATE),
        }


@dataclass
class ToolCallAgg:
    """Aggregates across (role, model_family, tool_name)."""
    counts: Counter = field(default_factory=Counter)          # key -> count
    errors: Counter = field(default_factory=Counter)          # key -> error count
    denials: Counter = field(default_factory=Counter)         # key -> permission-denial count
    result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))  # key -> SizeStats
    # repeated identical calls: (role, session_id, tool_name, input_sig) -> count
    call_signatures: Counter = field(default_factory=Counter)

    def key(self, role: str, family: str, tool: str) -> tuple:
        return (role, family, tool)

    def record_call(self, role: str, family: str, tool: str, session_id: str, tool_input: dict) -> None:
        k = self.key(role, family, tool)
        self.counts[k] += 1
        sig = (role, session_id, tool, input_signature(tool, tool_input))
        self.call_signatures[sig] += 1

    def record_result(self, role: str, family: str, tool: str, chars: int, is_error: bool, denied: bool) -> None:
        k = self.key(role, family, tool)
        self.result_sizes[k].add(chars)
        if is_error:
            self.errors[k] += 1
        if denied:
            self.denials[k] += 1


@dataclass
class AdoptionAgg:
    """SpecScore/CodeGrapher adoption: direct CLI/skill/MCP use, and the
    'missed opportunity' heuristics for generic-tool substitutes. Every
    counter here is keyed by (role, model family) plus a short label --
    never by the raw pattern or path that was classified."""
    direct_cli_calls: Counter = field(default_factory=Counter)        # (role, family, tool, subcommand) -> count
    direct_cli_result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))  # (role, family, tool) -> SizeStats
    skill_invocations: Counter = field(default_factory=Counter)       # (role, family, tool) -> count
    mcp_calls: Counter = field(default_factory=Counter)                # (role, family, tool_name) -> count

    grep_pattern_classes: Counter = field(default_factory=Counter)     # (role, family, class) -> count
    grep_pattern_result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))  # (role,family,class)->SizeStats
    grep_to_read_chains: Counter = field(default_factory=Counter)      # (role, family) -> chain count
    grep_to_read_calls_saved: Counter = field(default_factory=Counter)  # (role, family) -> calls saved estimate

    spec_path_classes: Counter = field(default_factory=Counter)        # (role, family, class) -> count
    spec_path_result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))  # (role,family,class)->SizeStats
    spec_hunt_via_git_or_grep: Counter = field(default_factory=Counter)  # (role, family) -> count

    def record_direct_cli(self, role: str, family: str, tool: str, subcommand: str) -> None:
        self.direct_cli_calls[(role, family, tool, subcommand)] += 1

    def record_direct_cli_result(self, role: str, family: str, tool: str, chars: int) -> None:
        self.direct_cli_result_sizes[(role, family, tool)].add(chars)

    def record_skill(self, role: str, family: str, tool: str) -> None:
        self.skill_invocations[(role, family, tool)] += 1

    def record_mcp(self, role: str, family: str, tool_name: str) -> None:
        self.mcp_calls[(role, family, tool_name)] += 1

    def record_grep_pattern(self, role: str, family: str, cls: str, chars: Optional[int] = None) -> None:
        self.grep_pattern_classes[(role, family, cls)] += 1
        if chars is not None:
            self.grep_pattern_result_sizes[(role, family, cls)].add(chars)

    def record_spec_path(self, role: str, family: str, cls: str, chars: Optional[int] = None) -> None:
        self.spec_path_classes[(role, family, cls)] += 1
        if chars is not None:
            self.spec_path_result_sizes[(role, family, cls)].add(chars)


PERMISSION_DENIAL_HINTS = re.compile(
    r"permission denied|requested permissions have not been granted|user rejected|"
    r"did not allow|blocked by (a )?hook|not permitted",
    re.IGNORECASE,
)


def result_content_len(content: Any) -> int:
    """Character length of a tool_result's content, without ever storing
    the content itself. Handles both string and structured-block forms."""
    if isinstance(content, str):
        return len(content)
    if isinstance(content, list):
        total = 0
        for block in content:
            if isinstance(block, dict):
                if isinstance(block.get("text"), str):
                    total += len(block["text"])
                elif isinstance(block.get("content"), str):
                    total += len(block["content"])
        return total
    if content is None:
        return 0
    try:
        return len(json.dumps(content))
    except Exception:
        return 0


def result_content_text_for_denial_check(content: Any) -> str:
    """Only used transiently to test a short regex for a denial hint; the
    matched text itself is never stored or written out."""
    if isinstance(content, str):
        return content[:400]
    if isinstance(content, list):
        for block in content:
            if isinstance(block, dict) and isinstance(block.get("text"), str):
                return block["text"][:400]
            if isinstance(block, dict) and isinstance(block.get("content"), str):
                return block["content"][:400]
    return ""


# --------------------------------------------------------------------------
# JSONL streaming
# --------------------------------------------------------------------------

def iter_jsonl(path: str) -> Iterator[dict]:
    """Stream a JSONL file one line at a time. Skips unparsable lines."""
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not isinstance(obj, dict):
                    continue  # a bare JSON string/number/list line is not a record
                yield obj
    except OSError as e:
        eprint(f"warn: cannot read {path}: {e}")
        return


WAITING_RE = re.compile(r"waiting for (a )?notification", re.IGNORECASE)
AUTOBG_TIMEOUT_RE = re.compile(r"(timed out|exceeded).{0,40}(foreground|timeout).{0,40}background|"
                                r"running in the background|moved to background", re.IGNORECASE)


def extract_text(content: Any) -> str:
    """Concatenate any text blocks in a message content list (for pattern
    matching only -- text itself is never written to the output pack)."""
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ""
    out = []
    for block in content:
        if not isinstance(block, dict):
            continue
        if block.get("type") == "text" and isinstance(block.get("text"), str):
            out.append(block["text"])
        if block.get("type") == "tool_result":
            c = block.get("content")
            if isinstance(c, str):
                out.append(c)
    return "\n".join(out)


# --------------------------------------------------------------------------
# Bash sequence mining (n-grams of classified verbs, per session)
# --------------------------------------------------------------------------

class SequenceMiner:
    def __init__(self, max_n: int = 4, min_n: int = 2):
        self.max_n = max_n
        self.min_n = min_n
        self.ngram_counts: Counter = Counter()

    def feed_session(self, verbs_in_order: list) -> None:
        for n in range(self.min_n, self.max_n + 1):
            for i in range(len(verbs_in_order) - n + 1):
                gram = tuple(verbs_in_order[i:i + n])
                self.ngram_counts[gram] += 1

    def top(self, k: int = 10) -> list:
        # only keep sequences seen more than once -- a single occurrence is
        # not a "pattern"
        repeated = [(g, c) for g, c in self.ngram_counts.items() if c >= 2]
        repeated.sort(key=lambda gc: (-gc[1], -len(gc[0])))
        return [{"sequence": list(g), "count": c} for g, c in repeated[:k]]


# --------------------------------------------------------------------------
# Main-session transcript pass
# --------------------------------------------------------------------------

@dataclass
class SessionStats:
    session_id: str
    turns: int = 0
    compactions: int = 0
    compactions_via_boundary: int = 0
    compactions_via_summary_flag: int = 0
    first_turn_ctx: Optional[int] = None
    last_turn_ctx: Optional[int] = None
    ctx_series: list = field(default_factory=list)
    day_usage: dict = field(default_factory=lambda: defaultdict(UsageBucket))
    model_usage: dict = field(default_factory=lambda: defaultdict(UsageBucket))
    tool_use_counts: Counter = field(default_factory=Counter)
    bash_verbs: Counter = field(default_factory=Counter)
    bash_long_count: int = 0
    bash_long_total_s: float = 0.0
    autobg_count: int = 0
    autobg_timeout_count: int = 0
    dispatch_count: int = 0
    resume_count: int = 0
    cpu_heavy_total: int = 0
    cpu_heavy_unwrapped: int = 0
    loop_count: int = 0
    loop_total_s: float = 0.0
    piped_bounded: int = 0
    piped_unbounded: int = 0


def process_transcript(path: str, since: datetime, until: datetime, role: str,
                        session_id: str, tool_agg: ToolCallAgg,
                        bash_verb_totals: Counter, bash_subverb_totals: Counter,
                        long_bash: list, autobg_total: list, autobg_timeout_total: list,
                        dispatch_targets: Counter, resume_targets: Counter,
                        cpu_heavy_counter: Counter, loop_events: list,
                        pipe_counter: Counter, seq_miner: SequenceMiner,
                        adoption_agg: Optional["AdoptionAgg"] = None,
                        default_model_family: str = "unknown") -> Optional[SessionStats]:
    """Shared pass over one transcript file (main session or subagent),
    tagged with `role` ("main" or "subagent") for the tool-call breakdown."""
    st = SessionStats(session_id=session_id)
    pending_tool_use: dict = {}  # tool_use_id -> (timestamp, name, input, adoption_meta)
    touched = False
    session_bash_order: list = []
    prev_ts: Optional[datetime] = None
    prev_text = ""
    cur_family = default_model_family
    symbol_lookup_ttl = 0  # calls remaining in which a Read "consumes" a pending symbol-like grep
    # One API response is often split across several JSONL lines (one per
    # content block), each repeating the SAME usage object. Count usage only
    # once per (requestId, falling back to message.id).
    seen_request_ids: set = set()
    compactions_via_boundary = 0
    compactions_via_summary_flag = 0

    for rec in iter_jsonl(path):
        ts = parse_ts(rec.get("timestamp"))
        rtype = rec.get("type")

        if rtype == "system" and rec.get("subtype") == "turn_duration" and role == "main":
            if ts and in_window(ts, since, until):
                st.turns += 1
                touched = True
            continue

        if rtype == "system" and rec.get("subtype") == "compact_boundary":
            if ts and in_window(ts, since, until):
                compactions_via_boundary += 1
                touched = True
            continue

        if rtype not in ("assistant", "user"):
            continue
        if role == "main" and rec.get("isSidechain"):
            continue
        msg = rec.get("message") or {}

        if rtype == "assistant":
            if ts is None or not in_window(ts, since, until):
                continue
            touched = True
            model = msg.get("model")
            if model:
                cur_family = model_family(model)
            if msg.get("isCompactSummary"):
                compactions_via_summary_flag += 1
            usage = msg.get("usage") or {}
            request_id = rec.get("requestId") or msg.get("id")
            already_counted = request_id is not None and request_id in seen_request_ids
            if usage and not already_counted:
                if request_id is not None:
                    seen_request_ids.add(request_id)
                st.day_usage[day_key(ts)].add(usage)
                st.model_usage[cur_family].add(usage)
                ctx = (int(usage.get("input_tokens") or 0)
                       + int(usage.get("cache_read_input_tokens") or 0)
                       + int(usage.get("cache_creation_input_tokens") or 0))
                st.ctx_series.append(ctx)
                if st.first_turn_ctx is None:
                    st.first_turn_ctx = ctx
                st.last_turn_ctx = ctx

            text = extract_text(msg.get("content"))
            if ts and prev_ts and (ts - prev_ts).total_seconds() > STALL_THRESHOLD_SECONDS:
                loop_events.append(("stall", role, session_id))
            if AUTOBG_TIMEOUT_RE.search(text or ""):
                st.autobg_timeout_count += 1
                autobg_timeout_total.append(1)
            prev_ts, prev_text = ts, text

            for block in msg.get("content") or []:
                if not isinstance(block, dict):
                    continue
                if block.get("type") == "tool_use":
                    name = block.get("name") or "?"
                    tinput = block.get("input") or {}
                    st.tool_use_counts[name] += 1
                    tool_agg.record_call(role, cur_family, name, session_id, tinput)
                    adoption_meta = None  # (kind, class_label) to resolve against the tool_result size

                    if name == "Bash":
                        cmd = tinput.get("command") or ""
                        verb, subverb = leading_verb_and_subverb(cmd)
                        coarse = classify_bash_verb(cmd)
                        st.bash_verbs[coarse] += 1
                        bash_verb_totals[coarse] += 1
                        bash_subverb_totals[subverb] += 1
                        session_bash_order.append(coarse)

                        if is_cpu_heavy(cmd):
                            st.cpu_heavy_total += 1
                            cpu_heavy_counter["total"] += 1
                            if not is_wb_run_wrapped(cmd):
                                st.cpu_heavy_unwrapped += 1
                                cpu_heavy_counter["unwrapped"] += 1

                        if is_hand_rolled_loop(cmd):
                            loop_events.append(("loop", role, session_id, ts))

                        bounded = has_bounded_pipe(cmd)
                        if bounded is True:
                            st.piped_bounded += 1
                            pipe_counter["bounded"] += 1
                        elif bounded is False:
                            st.piped_unbounded += 1
                            pipe_counter["unbounded"] += 1

                        if tinput.get("run_in_background") is True:
                            st.autobg_count += 1
                            autobg_total.append(1)

                        if adoption_agg is not None:
                            raw_verb = (cmd.strip().split() or [""])[0]
                            if raw_verb in CLI_KNOWLEDGE_TOOLS:
                                sub = _sanitize_verb((cmd.strip().split() + [""])[1])
                                adoption_agg.record_direct_cli(role, cur_family, raw_verb, sub)
                                adoption_meta = ("direct_cli", raw_verb)
                                symbol_lookup_ttl = 0  # a real CLI query resolves any pending lookup
                            elif re.match(r"^(git\s+grep|rg|grep)\b", cmd.strip()):
                                pattern = extract_bash_grep_pattern(cmd)
                                cls = classify_grep_pattern(pattern or "")
                                adoption_agg.record_grep_pattern(role, cur_family, cls)
                                adoption_meta = ("grep", cls)
                                if cls == "symbol-like":
                                    symbol_lookup_ttl = 3
                                if "spec/" in cmd:
                                    adoption_agg.spec_hunt_via_git_or_grep[(role, cur_family)] += 1
                            elif subverb == "git log" and "spec/" in cmd:
                                adoption_agg.spec_hunt_via_git_or_grep[(role, cur_family)] += 1
                            else:
                                read_path = extract_bash_read_path(cmd)
                                if read_path:
                                    spec_cls = classify_spec_path(read_path)
                                    if spec_cls:
                                        adoption_agg.record_spec_path(role, cur_family, spec_cls)
                                        adoption_meta = ("spec_path", spec_cls)
                                    elif looks_like_source_file(read_path) and symbol_lookup_ttl > 0:
                                        adoption_agg.grep_to_read_chains[(role, cur_family)] += 1
                                        adoption_agg.grep_to_read_calls_saved[(role, cur_family)] += 1
                                        symbol_lookup_ttl = 0

                    if name == "Grep" and adoption_agg is not None:
                        pattern = tinput.get("pattern")
                        if pattern is not None:
                            cls = classify_grep_pattern(pattern)
                            adoption_agg.record_grep_pattern(role, cur_family, cls)
                            adoption_meta = ("grep", cls)
                            if cls == "symbol-like":
                                symbol_lookup_ttl = 3
                            spec_target = tinput.get("path") or tinput.get("glob") or ""
                            if "spec/" in spec_target:
                                adoption_agg.spec_hunt_via_git_or_grep[(role, cur_family)] += 1

                    if name == "Read" and adoption_agg is not None:
                        file_path = tinput.get("file_path") or ""
                        spec_cls = classify_spec_path(file_path)
                        if spec_cls:
                            adoption_agg.record_spec_path(role, cur_family, spec_cls)
                            adoption_meta = ("spec_path", spec_cls)
                        elif looks_like_source_file(file_path) and symbol_lookup_ttl > 0:
                            adoption_agg.grep_to_read_chains[(role, cur_family)] += 1
                            adoption_agg.grep_to_read_calls_saved[(role, cur_family)] += 1
                            symbol_lookup_ttl = 0

                    if symbol_lookup_ttl > 0 and name not in ("Bash", "Grep", "Read"):
                        symbol_lookup_ttl -= 1

                    if name == "Skill" and adoption_agg is not None:
                        skill_name = str(tinput.get("skill") or "")
                        if skill_name.startswith("codegrapher"):
                            adoption_agg.record_skill(role, cur_family, "codegrapher")
                        elif skill_name.startswith("specscore"):
                            adoption_agg.record_skill(role, cur_family, "specscore")

                    if name.startswith("mcp__codegrapher") and adoption_agg is not None:
                        adoption_agg.record_mcp(role, cur_family, name)

                    pending_tool_use[block.get("id")] = (ts, name, tinput, adoption_meta)

                    if name == "Agent":
                        st.dispatch_count += 1
                        dispatch_targets[tinput.get("subagent_type") or "unspecified"] += 1
                    if name == "SendMessage":
                        if tinput.get("to"):
                            st.resume_count += 1
                            resume_targets[str(tinput.get("to"))[:40]] += 1

        else:  # user message: tool_result timing, error/denial, size
            content = msg.get("content")
            if isinstance(content, list):
                for block in content:
                    if not isinstance(block, dict) or block.get("type") != "tool_result":
                        continue
                    tid = block.get("tool_use_id")
                    entry = pending_tool_use.get(tid)
                    if not entry:
                        continue
                    start, name, tinput, adoption_meta = entry
                    is_error = bool(block.get("is_error"))
                    size = result_content_len(block.get("content"))
                    denial_text = result_content_text_for_denial_check(block.get("content")) if is_error else ""
                    denied = bool(denial_text and PERMISSION_DENIAL_HINTS.search(denial_text))
                    tool_agg.record_result(role, cur_family, name, size, is_error, denied)
                    if adoption_agg is not None and adoption_meta:
                        kind, label = adoption_meta
                        if kind == "direct_cli":
                            adoption_agg.record_direct_cli_result(role, cur_family, label, size)
                        elif kind == "grep":
                            adoption_agg.grep_pattern_result_sizes[(role, cur_family, label)].add(size)
                        elif kind == "spec_path":
                            adoption_agg.spec_path_result_sizes[(role, cur_family, label)].add(size)
                    if name == "Bash" and start and ts:
                        dur = (ts - start).total_seconds()
                        if dur >= 30:
                            st.bash_long_count += 1
                            st.bash_long_total_s += dur
                            long_bash.append(dur)
                        # attach duration to the most recent matching loop event, best-effort
                        cmd = tinput.get("command") or ""
                        if is_hand_rolled_loop(cmd):
                            loop_events.append(("loop_duration", role, session_id, dur))

    if session_bash_order:
        seq_miner.feed_session(session_bash_order)

    if not touched:
        return None
    st.compactions_via_boundary = compactions_via_boundary
    st.compactions_via_summary_flag = compactions_via_summary_flag
    # The two signals should mark the same events; take the max rather than
    # summing to avoid double-counting when both are present.
    st.compactions = max(compactions_via_boundary, compactions_via_summary_flag)
    return st


# --------------------------------------------------------------------------
# Agent (subagent) transcript pass -- wraps process_transcript, plus
# per-agent duration/tool-use/stall bookkeeping used by the "subagents"
# section of metrics.json.
# --------------------------------------------------------------------------

@dataclass
class AgentStats:
    agent_id: str
    source: str
    model: str = "unknown"
    first_ts: Optional[datetime] = None
    last_ts: Optional[datetime] = None
    usage: UsageBucket = field(default_factory=UsageBucket)
    tool_uses: int = 0
    stalls: int = 0
    stall_ending_waiting: int = 0
    dispatch_tool_use_id: Optional[str] = None
    agent_type: Optional[str] = None
    description_present: bool = False


def read_agent_meta(path: str) -> dict:
    """Read the sibling agent-<id>.meta.json next to a subagent transcript,
    if present. Carries the dispatcher's declared model, description and
    the toolUseId that joins this agent back to the parent's Agent call."""
    meta_path = re.sub(r"\.jsonl$", ".meta.json", path)
    if not os.path.exists(meta_path):
        return {}
    try:
        return json.loads(Path(meta_path).read_text(encoding="utf-8"))
    except Exception:
        return {}


def process_agent_transcript(path: str, since: datetime, until: datetime, source: str) -> Optional[AgentStats]:
    agent_id = os.path.basename(path).replace("agent-", "").replace(".jsonl", "").replace(".output", "")
    a = AgentStats(agent_id=agent_id, source=source)
    meta = read_agent_meta(path)
    a.dispatch_tool_use_id = meta.get("toolUseId")
    models = Counter()
    prev_ts: Optional[datetime] = None
    prev_text = ""
    touched = False
    seen_request_ids: set = set()

    for rec in iter_jsonl(path):
        ts = parse_ts(rec.get("timestamp"))
        if rec.get("type") != "assistant":
            continue
        msg = rec.get("message") or {}
        model = msg.get("model")
        if model:
            models[model] += 1
        usage = msg.get("usage") or {}
        request_id = rec.get("requestId") or msg.get("id")
        already_counted = request_id is not None and request_id in seen_request_ids
        if ts and in_window(ts, since, until):
            touched = True
            if usage and not already_counted:
                if request_id is not None:
                    seen_request_ids.add(request_id)
                a.usage.add(usage)
            if a.first_ts is None or ts < a.first_ts:
                a.first_ts = ts
            if a.last_ts is None or ts > a.last_ts:
                a.last_ts = ts
        for block in msg.get("content") or []:
            if isinstance(block, dict) and block.get("type") == "tool_use":
                a.tool_uses += 1
        text = extract_text(msg.get("content"))
        if ts and prev_ts and (ts - prev_ts).total_seconds() > STALL_THRESHOLD_SECONDS:
            a.stalls += 1
            if WAITING_RE.search(prev_text or ""):
                a.stall_ending_waiting += 1
        if ts:
            prev_ts, prev_text = ts, text

    if not touched:
        return None
    # meta.json's declared model is authoritative when present (it is what
    # the dispatcher asked for); the transcript's majority-vote model is the
    # fallback for older transcripts with no meta.json sidecar.
    if meta.get("model"):
        a.model = str(meta["model"])
    elif models:
        a.model = models.most_common(1)[0][0]
    a.agent_type = meta.get("agentType")
    a.description_present = bool(meta.get("description"))
    return a


def discover_subagent_files(home: Path) -> Iterable[tuple]:
    """Yield (path, source_label) for every subagent transcript file,
    deduped by agent id (prefer the durable ~/.claude/projects copy over
    the ephemeral /tmp copy of the same agent)."""
    projects_root = home / ".claude" / "projects"
    seen_ids: set = set()

    for p in sorted(glob.glob(str(projects_root / "*" / "*" / "subagents" / "*.jsonl"))):
        agent_id = os.path.basename(p).replace("agent-", "").replace(".jsonl", "")
        if agent_id in seen_ids:
            continue
        seen_ids.add(agent_id)
        yield p, "subagents_dir"

    tmp_root = Path(os.environ.get("SDLC_METRICS_TMP_ROOT", "/tmp"))
    for p in sorted(glob.glob(str(tmp_root / "claude-*" / "*" / "*" / "tasks" / "*.output"))):
        agent_id = os.path.basename(p).replace(".output", "")
        if agent_id in seen_ids:
            continue
        seen_ids.add(agent_id)
        yield p, "tmp_task_output"


def discover_main_sessions(home: Path) -> list:
    projects_root = home / ".claude" / "projects"
    return sorted(glob.glob(str(projects_root / "*" / "*.jsonl")))


# --------------------------------------------------------------------------
# WB worklog pass
# --------------------------------------------------------------------------

def process_wb_worklogs(wb_state_dirs: list, since: datetime, until: datetime,
                         raw_dir: Optional[Path] = None) -> dict:
    """Scan one or more WB state directories (a fleet may have used
    `~/.wb` and later `~/projects/.wb`, or both at once) and merge them,
    de-duplicating events seen under more than one root by (type, claim_id,
    run_id)."""
    existing_dirs = [d for d in wb_state_dirs if d.exists()]
    if not existing_dirs:
        return {"unavailable": f"no WB state dir found among {[str(d) for d in wb_state_dirs]}"}

    out = {
        "state_dirs_scanned": [str(d) for d in existing_dirs],
        "claimed": 0, "sealed": 0,
        "disposition": Counter(),
        "landing_durations_s": [],
    }

    claims: dict = {}
    seen_events: set = set()
    for wb_state_dir in existing_dirs:
        files = sorted(glob.glob(str(wb_state_dir / "worklogs" / "*" / "outbox" / "*.json")))
        for fp in files:
            try:
                d = json.loads(Path(fp).read_text(encoding="utf-8"))
            except Exception:
                continue
            t = d.get("type")
            dedupe_key = (t, d.get("claim_id"), d.get("run_id"))
            if dedupe_key in seen_events:
                continue
            seen_events.add(dedupe_key)
            ts = parse_ts(d.get("at"))
            if t == "worktree.claimed":
                out["claimed"] += 1
                if ts:
                    claims[d.get("claim_id")] = ts
            elif t == "worktree.sealed":
                if ts and not in_window(ts, since, until):
                    continue
                out["sealed"] += 1
                out["disposition"][d.get("disposition") or "unknown"] += 1
                claimed_at = claims.get(d.get("claim_id"))
                if claimed_at and ts:
                    out["landing_durations_s"].append((ts - claimed_at).total_seconds())

    out["disposition"] = dict(out["disposition"])
    if out["landing_durations_s"]:
        out["landing_duration_median_s"] = statistics.median(out["landing_durations_s"])
        out["landing_duration_p90_s"] = (
            sorted(out["landing_durations_s"])[int(0.9 * (len(out["landing_durations_s"]) - 1))]
        )
    del out["landing_durations_s"]

    wait_kinds = Counter()
    any_waits_dir = False
    for wb_state_dir in existing_dirs:
        waits_dir = wb_state_dir / "waits"
        if not waits_dir.exists():
            continue
        any_waits_dir = True
        for fp in sorted(glob.glob(str(waits_dir / "*.json"))):
            try:
                d = json.loads(Path(fp).read_text(encoding="utf-8"))
            except Exception:
                continue
            wait_kinds[d.get("kind") or "unknown"] += 1
    if any_waits_dir:
        out["open_wait_snapshots_by_kind"] = dict(wait_kinds)
        out["open_wait_note"] = (
            "counts files present on disk at extraction time, not all waits started "
            "within the window; wb keeps no durable wait-history log"
        )
    else:
        out["open_wait_snapshots_by_kind"] = "unavailable: no waits dir found under any scanned state dir"

    out["admission_queue"] = "unavailable: wb run admission/queue history is not persisted to disk"
    out["refusal_codes"] = "unavailable: worklog events carry disposition, not a structured refusal-code taxonomy"

    out["hook_events"] = process_hook_events(Path.home() / ".local" / "state" / "wb" / "hook-events.jsonl",
                                              since, until)
    out["surviving_run_events"] = process_surviving_run_events(home_projects_root(existing_dirs),
                                                                 since, until, raw_dir)
    return out


def home_projects_root(existing_wb_state_dirs: list) -> Path:
    """Best-effort projects root to search for surviving worktree run-event
    files: the parent of whichever scanned .wb dir looks like <home>/projects/.wb."""
    for d in existing_wb_state_dirs:
        if d.name == ".wb" and d.parent.name == "projects":
            return d.parent
    return existing_wb_state_dirs[0].parent if existing_wb_state_dirs else Path.home() / "projects"


def process_hook_events(path: Path, since: datetime, until: datetime) -> dict:
    if not path.exists():
        return {"unavailable": f"hook-events log not found at {path}"}
    total = 0
    in_window_count = 0
    by_outcome = Counter()
    for rec in iter_jsonl(str(path)):
        total += 1
        ts = parse_ts(rec.get("timestamp"))
        if not in_window(ts, since, until):
            continue
        in_window_count += 1
        by_outcome[rec.get("outcome") or "unknown"] += 1
    return {
        "events_on_disk_total": total,
        "events_in_window": in_window_count,
        "by_outcome_in_window": dict(by_outcome),
    }


def process_surviving_run_events(projects_root: Path, since: datetime, until: datetime,
                                  raw_dir: Optional[Path]) -> dict:
    """`wb run` command-mode receipts live at <worktree>/.wb/local/run/events.jsonl
    and are deleted with the worktree, so only whatever worktrees still exist
    at extraction time can be read. This is a lower bound, not a full count."""
    # worktree layout is <task>/<host>/<owner>/<repo>/.wb/local/run/events.jsonl
    # (e.g. .worktrees/my-task/github.com/sneat-dev/wb/.wb/...); glob
    # recursively rather than hard-coding the segment count, which has
    # changed before (see the wb layout-migrations log).
    files = sorted(glob.glob(str(projects_root / ".worktrees" / "**" / ".wb" / "local" / "run" / "events.jsonl"),
                              recursive=True))
    if not files:
        return {"unavailable": f"no surviving <worktree>/.wb/local/run/events.jsonl files under {projects_root}/.worktrees"}
    total = 0
    in_window_count = 0
    by_state = Counter()
    by_kind = Counter()
    queue_waits_ms = []
    for fp in files:
        for rec in iter_jsonl(fp):
            total += 1
            ts = parse_ts(rec.get("timestamp"))
            if not in_window(ts, since, until):
                continue
            in_window_count += 1
            by_state[rec.get("state") or "unknown"] += 1
            by_kind[rec.get("kind") or "unknown"] += 1
            qw = rec.get("queue_wait_ms")
            if isinstance(qw, (int, float)):
                queue_waits_ms.append(qw)
        if raw_dir:
            dest_dir = raw_dir / "wb_run_events"
            dest_dir.mkdir(parents=True, exist_ok=True)
            try:
                dest = dest_dir / (hashlib.sha1(fp.encode()).hexdigest()[:16] + ".jsonl")
                dest.write_bytes(Path(fp).read_bytes())
            except Exception:
                pass
    return {
        "surviving_files": len(files),
        "events_on_disk_total": total,
        "events_in_window": in_window_count,
        "by_state_in_window": dict(by_state),
        "by_kind_in_window": dict(by_kind.most_common(20)),
        "queue_wait_ms_median": statistics.median(queue_waits_ms) if queue_waits_ms else None,
        "note": "lower bound only: events for any worktree already cleaned up before this "
                "extraction ran are gone",
    }


# --------------------------------------------------------------------------
# GitHub pass
# --------------------------------------------------------------------------

def gh_api_paginated(endpoint: str, params: Optional[dict] = None, cache_path: Optional[Path] = None,
                      offline: bool = False) -> list:
    if offline:
        if cache_path and cache_path.exists():
            return json.loads(cache_path.read_text(encoding="utf-8"))
        raise RuntimeError(f"--offline given but no cached raw/ response for {endpoint}")

    args = ["gh", "api", endpoint, "--paginate", "--method", "GET"]
    if params:
        for k, v in params.items():
            args += ["-f", f"{k}={v}"]
    try:
        proc = subprocess.run(args, capture_output=True, text=True, timeout=180)
    except Exception as e:
        if cache_path and cache_path.exists():
            return json.loads(cache_path.read_text(encoding="utf-8"))
        raise RuntimeError(f"gh api {endpoint} failed: {e}")
    if proc.returncode != 0:
        if cache_path and cache_path.exists():
            return json.loads(cache_path.read_text(encoding="utf-8"))
        raise RuntimeError(f"gh api {endpoint} exit {proc.returncode}: {proc.stderr[:400]}")

    text = proc.stdout.strip()
    items: list = []
    dec = json.JSONDecoder()
    idx = 0
    while idx < len(text):
        while idx < len(text) and text[idx] in " \n\t\r":
            idx += 1
        if idx >= len(text):
            break
        obj, end = dec.raw_decode(text, idx)
        if isinstance(obj, list):
            items.extend(obj)
        else:
            items.append(obj)
        idx = end
    if cache_path:
        cache_path.parent.mkdir(parents=True, exist_ok=True)
        cache_path.write_text(json.dumps(items), encoding="utf-8")
    return items


def process_github(repo: str, since: datetime, until: datetime, raw_dir: Path, offline: bool) -> dict:
    result: dict = {}
    try:
        prs = gh_api_paginated(
            f"repos/{repo}/pulls",
            params={"state": "all", "sort": "created", "direction": "desc", "per_page": "100"},
            cache_path=raw_dir / "pulls.json", offline=offline,
        )
    except Exception as e:
        return {"unavailable": f"gh api pulls failed: {e}"}

    window_prs = []
    for pr in prs:
        created = parse_ts(pr.get("created_at"))
        if created and created < since - timedelta(days=60):
            break
        if created and in_window(created, since, until):
            window_prs.append(pr)

    merge_times_h = []
    merged, closed_unmerged, still_open = 0, 0, 0
    for pr in window_prs:
        if pr.get("merged_at"):
            merged += 1
            c = parse_ts(pr.get("created_at"))
            m = parse_ts(pr.get("merged_at"))
            if c and m:
                merge_times_h.append((m - c).total_seconds() / 3600.0)
        elif pr.get("closed_at"):
            closed_unmerged += 1
        else:
            still_open += 1

    result["prs"] = {
        "created_in_window": len(window_prs),
        "merged": merged,
        "closed_unmerged": closed_unmerged,
        "still_open": still_open,
        "time_to_merge_hours": {
            "median": round(statistics.median(merge_times_h), 2) if merge_times_h else None,
            "p90": round(sorted(merge_times_h)[int(0.9 * (len(merge_times_h) - 1))], 2) if merge_times_h else None,
            "count": len(merge_times_h),
        },
    }

    try:
        runs = gh_api_paginated(
            f"repos/{repo}/actions/runs",
            params={"per_page": "100"},
            cache_path=raw_dir / "actions_runs.json", offline=offline,
        )
    except Exception as e:
        result["actions"] = {"unavailable": f"gh api actions/runs failed: {e}"}
        return result

    flat_runs = []
    for item in runs:
        if isinstance(item, dict) and "workflow_runs" in item:
            flat_runs.extend(item["workflow_runs"])
        elif isinstance(item, dict) and "id" in item and "conclusion" in item:
            flat_runs.append(item)

    by_workflow_durations: dict = defaultdict(list)
    cancelled = 0
    failed = 0
    wasted_minutes = 0.0
    by_event = Counter()
    window_runs = 0
    for r in flat_runs:
        created = parse_ts(r.get("created_at"))
        if not created or not in_window(created, since, until):
            continue
        window_runs += 1
        wf = r.get("name") or "unknown"
        by_event[r.get("event") or "unknown"] += 1
        start = parse_ts(r.get("run_started_at")) or created
        end = parse_ts(r.get("updated_at"))
        dur_min = (end - start).total_seconds() / 60.0 if start and end else None
        if dur_min is not None:
            by_workflow_durations[wf].append(dur_min)
        concl = r.get("conclusion")
        if concl == "cancelled":
            cancelled += 1
            if dur_min:
                wasted_minutes += dur_min
        elif concl == "failure":
            failed += 1
            if dur_min:
                wasted_minutes += dur_min

    workflow_summary = {}
    for wf, durs in by_workflow_durations.items():
        workflow_summary[wf] = {
            "runs": len(durs),
            "median_minutes": round(statistics.median(durs), 2),
            "p90_minutes": round(sorted(durs)[int(0.9 * (len(durs) - 1))], 2) if len(durs) > 1 else round(durs[0], 2),
        }

    result["actions"] = {
        "runs_in_window": window_runs,
        "cancelled": cancelled,
        "failed": failed,
        "wasted_minutes_cancelled_plus_failed": round(wasted_minutes, 1),
        "by_event": dict(by_event),
        "by_workflow": workflow_summary,
    }
    result["pipeline_inference_note"] = (
        "review/fix rounds and lint-vs-test failure attribution are not directly "
        "recorded by GitHub; only run-level conclusion and count are available, so "
        "per-PR failure-round counts are marked unavailable rather than guessed"
    )
    return result


# --------------------------------------------------------------------------
# git log pass
# --------------------------------------------------------------------------

def process_git_log(repo_path: Path, since: datetime, until: datetime) -> dict:
    if not repo_path.exists():
        return {"unavailable": f"repo not found at {repo_path}"}
    since_s = since.strftime("%Y-%m-%d %H:%M:%S")
    until_s = until.strftime("%Y-%m-%d %H:%M:%S")
    try:
        total = subprocess.run(
            ["git", "-C", str(repo_path), "log", f"--since={since_s}", f"--until={until_s}", "--oneline"],
            capture_output=True, text=True, timeout=60,
        )
        merges = subprocess.run(
            ["git", "-C", str(repo_path), "log", f"--since={since_s}", f"--until={until_s}", "--merges", "--oneline"],
            capture_output=True, text=True, timeout=60,
        )
    except Exception as e:
        return {"unavailable": f"git log failed: {e}"}
    if total.returncode != 0:
        return {"unavailable": f"git log exit {total.returncode}: {total.stderr[:200]}"}
    commit_lines = [l for l in total.stdout.splitlines() if l.strip()]
    merge_lines = [l for l in merges.stdout.splitlines() if l.strip()]
    pr_refs = len(re.findall(r"#\d+", total.stdout))
    return {
        "commits": len(commit_lines),
        "merge_commits": len(merge_lines),
        "pr_references": pr_refs,
    }


# --------------------------------------------------------------------------
# Codex harness pass (separate from the Claude Code harness above)
# --------------------------------------------------------------------------

def process_codex_sessions(home: Path, since: datetime, until: datetime) -> dict:
    """Codex CLI rollout files at <home>/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
    carry `token_count` event_msg entries with cumulative
    `total_token_usage` -- read as a separate harness's totals, not merged
    into the Claude Code token tables above."""
    root = home / ".codex" / "sessions"
    if not root.exists():
        return {"unavailable": f"no Codex sessions dir at {root}"}
    files = sorted(glob.glob(str(root / "*" / "*" / "*" / "rollout-*.jsonl")))
    if not files:
        return {"unavailable": f"no rollout-*.jsonl files under {root}"}

    sessions_touched = 0
    input_tokens = 0
    cached_input_tokens = 0
    cache_write_tokens = 0
    output_tokens = 0
    reasoning_output_tokens = 0

    for fp in files:
        # a session's usage is cumulative per file; take the LAST in-window
        # token_count event as that session's total, matching how the main
        # Claude Code pass treats a session as one unit.
        last_usage = None
        touched = False
        for rec in iter_jsonl(fp):
            ts = parse_ts(rec.get("timestamp"))
            if rec.get("type") != "event_msg":
                continue
            payload = rec.get("payload") or {}
            if payload.get("type") != "token_count":
                continue
            info = payload.get("info") or {}
            total_usage = info.get("total_token_usage")
            if not total_usage:
                continue
            if ts and in_window(ts, since, until):
                touched = True
                last_usage = total_usage
        if touched and last_usage:
            sessions_touched += 1
            input_tokens += int(last_usage.get("input_tokens") or 0)
            cached_input_tokens += int(last_usage.get("cached_input_tokens") or 0)
            cache_write_tokens += int(last_usage.get("cache_write_input_tokens") or 0)
            output_tokens += int(last_usage.get("output_tokens") or 0)
            reasoning_output_tokens += int(last_usage.get("reasoning_output_tokens") or 0)

    return {
        "sessions_touched_in_window": sessions_touched,
        "input_tokens": input_tokens,
        "cached_input_tokens": cached_input_tokens,
        "cache_write_input_tokens": cache_write_tokens,
        "output_tokens": output_tokens,
        "reasoning_output_tokens": reasoning_output_tokens,
        "note": "Codex CLI rollout token_count is cumulative per session; the last "
                "in-window snapshot per rollout file is taken as that session's total. "
                "No per-model price table is applied here (Codex model pricing is out of "
                "scope for PRICE_TABLE_USD_PER_MTOK, which covers the Claude family only)",
    }


# --------------------------------------------------------------------------
# Tool-call section builder
# --------------------------------------------------------------------------

def build_tools_section(tool_agg: ToolCallAgg) -> dict:
    by_role_model_tool = {}
    top_offenders = []
    for k, count in tool_agg.counts.items():
        role, family, tool = k
        sizes = tool_agg.result_sizes.get(k)
        summary = sizes.summary() if sizes else {"count": 0, "total_chars": 0, "p50_chars": 0, "p95_chars": 0, "max_chars": 0}
        errors = tool_agg.errors.get(k, 0)
        denials = tool_agg.denials.get(k, 0)
        entry = {
            "count": count,
            "error_rate": round(errors / count, 3) if count else 0.0,
            "denied_count": denials,
            "result_size": summary,
        }
        by_role_model_tool.setdefault(role, {}).setdefault(family, {})[tool] = entry
        if summary["total_chars"] > 0:
            top_offenders.append({
                "role": role, "model": family, "tool": tool,
                "total_chars": summary["total_chars"],
                "estimated_total_tokens": summary["estimated_total_tokens"],
                "max_chars": summary["max_chars"],
                "calls": count,
            })
    top_offenders.sort(key=lambda e: -e["total_chars"])

    repeated = {k: c for k, c in tool_agg.call_signatures.items() if c >= 2}
    repeated_by_role_tool = Counter()
    repeated_calls_total = 0
    repeated_extra_calls = 0  # calls beyond the first, i.e. the "wasted" repeats
    for (role, session_id, tool, sig), c in repeated.items():
        repeated_by_role_tool[(role, tool)] += 1
        repeated_calls_total += c
        repeated_extra_calls += (c - 1)

    return {
        "by_role_model_tool": by_role_model_tool,
        "top_result_size_offenders": top_offenders[:15],
        "repeated_identical_calls": {
            "distinct_repeated_signatures": len(repeated),
            "extra_calls_beyond_first": repeated_extra_calls,
            "by_role_tool": {f"{role}:{tool}": n for (role, tool), n in repeated_by_role_tool.most_common(20)},
        },
    }


def build_bash_section(bash_verb_totals: Counter, bash_subverb_totals: Counter,
                        long_bash: list, autobg_total: list, autobg_timeout_total: list,
                        cpu_heavy_counter: Counter, loop_events: list,
                        pipe_counter: Counter, seq_miner: SequenceMiner) -> dict:
    loop_count = sum(1 for e in loop_events if e[0] == "loop")
    loop_durations = [e[3] for e in loop_events if e[0] == "loop_duration"]
    total = cpu_heavy_counter.get("total", 0)
    unwrapped = cpu_heavy_counter.get("unwrapped", 0)
    wb_run_coverage = None
    if total:
        wb_run_coverage = round(1 - (unwrapped / total), 3)

    return {
        "by_verb": dict(bash_verb_totals),
        "by_subverb": dict(bash_subverb_totals.most_common(40)),
        "long_commands_ge_30s": {
            "count": len(long_bash),
            "total_minutes": round(sum(long_bash) / 60.0, 1) if long_bash else 0.0,
        },
        "run_in_background_flag_count": len(autobg_total),
        "harness_autobackgrounded_count": len(autobg_timeout_total),
        "cpu_heavy_commands": {
            "total": total,
            "not_wrapped_in_wb_run": unwrapped,
            "wb_run_coverage": wb_run_coverage,
        },
        "hand_rolled_loops": {
            "count": loop_count,
            "total_minutes": round(sum(loop_durations) / 60.0, 1) if loop_durations else 0.0,
            "duration_samples": len(loop_durations),
        },
        "pipe_truncation": {
            "bounded_via_tail_or_head": pipe_counter.get("bounded", 0),
            "unbounded": pipe_counter.get("unbounded", 0),
        },
        "top_multi_call_sequences": seq_miner.top(10),
    }


def build_adoption_section(adoption_agg: AdoptionAgg, codegrapher_status: dict) -> dict:
    """SpecScore/CodeGrapher direct use vs. probable generic-tool
    substitutes, broken down per (role, model family)."""

    def by_role_model(counter: Counter, extra_key_name: Optional[str] = None) -> dict:
        out: dict = {}
        for key, count in counter.items():
            if extra_key_name:
                role, family, extra = key
                out.setdefault(role, {}).setdefault(family, {}).setdefault(extra_key_name, {})[extra] = count
            else:
                role, family = key
                out.setdefault(role, {})[family] = count
        return out

    def sizes_by_role_model(sizes: dict, extra_key_name: str) -> dict:
        out: dict = {}
        for key, stats in sizes.items():
            role, family, extra = key
            out.setdefault(role, {}).setdefault(family, {}).setdefault(extra_key_name, {})[extra] = stats.summary()
        return out

    direct_cli = {}
    for (role, family, tool, subcommand), count in adoption_agg.direct_cli_calls.items():
        direct_cli.setdefault(role, {}).setdefault(family, {}).setdefault(tool, {}).setdefault(
            "subcommands", {})[subcommand] = count
    direct_cli_sizes = {}
    for (role, family, tool), stats in adoption_agg.direct_cli_result_sizes.items():
        direct_cli_sizes.setdefault(role, {}).setdefault(family, {}).setdefault(tool, {})["result_size"] = stats.summary()

    return {
        "direct_use": {
            "cli_calls_by_role_model_tool": direct_cli,
            "cli_result_sizes_by_role_model_tool": direct_cli_sizes,
            "skill_invocations_by_role_model_tool": by_role_model(adoption_agg.skill_invocations, "tool"),
            "mcp_calls_by_role_model_tool": by_role_model(adoption_agg.mcp_calls, "tool"),
        },
        "missed_opportunities": {
            "code_structure_grep_by_role_model_class": by_role_model(adoption_agg.grep_pattern_classes, "class"),
            "code_structure_grep_result_sizes": sizes_by_role_model(adoption_agg.grep_pattern_result_sizes, "class"),
            "grep_to_read_chains_by_role_model": by_role_model(adoption_agg.grep_to_read_chains),
            "calls_plausibly_saved_by_role_model": by_role_model(adoption_agg.grep_to_read_calls_saved),
            "spec_lookup_by_role_model_class": by_role_model(adoption_agg.spec_path_classes, "class"),
            "spec_lookup_result_sizes": sizes_by_role_model(adoption_agg.spec_path_result_sizes, "class"),
            "spec_hunt_via_git_or_grep_by_role_model": by_role_model(adoption_agg.spec_hunt_via_git_or_grep),
        },
        "codegrapher_index_status": codegrapher_status,
        "note": (
            "code-structure and spec-lookup classes are heuristic (regex-based pattern/path "
            "classification); they estimate probable substitutes for a CLI query, not a "
            "certain one. Only the class label is ever recorded, never the pattern or path."
        ),
    }


def check_codegrapher_status(repo_path: Path) -> dict:
    """Runs `codegrapher status` for ONE repo (the one this pass already has
    a local path for, via --git-repo-path) as a CURRENT-STATE snapshot, not
    a historical one -- it reflects whether an index exists right now, not
    whether it existed during the measured window."""
    if not repo_path.exists():
        return {"unavailable": f"repo not found at {repo_path}"}
    try:
        proc = subprocess.run(["codegrapher", "status"], cwd=str(repo_path),
                               capture_output=True, text=True, timeout=30)
    except FileNotFoundError:
        return {"unavailable": "codegrapher CLI not found on PATH"}
    except Exception as e:
        return {"unavailable": f"codegrapher status failed: {e}"}
    out = (proc.stdout or "") + (proc.stderr or "")
    indexed = "not initialized" not in out.lower() and proc.returncode == 0
    return {
        "checked_repo": str(repo_path),
        "checked_at": datetime.now(timezone.utc).isoformat(),
        "indexed_now": indexed,
        "scope_note": "only the --git-repo-path repository was checked; per-repo status for "
                       "every repo touched in the window is out of scope for this pass",
    }


# --------------------------------------------------------------------------
# Orchestration
# --------------------------------------------------------------------------

def run_extract(since: datetime, until: datetime, out_dir: Path, home: Path,
                 repo: str, git_repo_path: Path, wb_state_dirs: list,
                 offline: bool) -> dict:
    t0 = time.time()
    out_dir.mkdir(parents=True, exist_ok=True)
    raw_dir = out_dir / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)

    tool_agg = ToolCallAgg()
    bash_verb_totals: Counter = Counter()
    bash_subverb_totals: Counter = Counter()
    long_bash: list = []
    autobg_total: list = []
    autobg_timeout_total: list = []
    dispatch_targets: Counter = Counter()
    resume_targets: Counter = Counter()
    cpu_heavy_counter: Counter = Counter()
    loop_events: list = []
    pipe_counter: Counter = Counter()
    seq_miner = SequenceMiner()
    adoption_agg = AdoptionAgg()

    global_day_usage: dict = defaultdict(UsageBucket)
    global_model_usage: dict = defaultdict(UsageBucket)

    sessions = []
    main_files = discover_main_sessions(home)
    for p in main_files:
        session_id = os.path.splitext(os.path.basename(p))[0]
        st = process_transcript(p, since, until, "main", session_id, tool_agg,
                                 bash_verb_totals, bash_subverb_totals, long_bash,
                                 autobg_total, autobg_timeout_total,
                                 dispatch_targets, resume_targets,
                                 cpu_heavy_counter, loop_events, pipe_counter, seq_miner,
                                 adoption_agg=adoption_agg)
        if st:
            sessions.append(st)
            for fam, bucket in st.model_usage.items():
                merge_bucket(global_model_usage[fam], bucket)
            # Per-day totals are recorded per session without a day*model
            # cross-tab (see the "daily token totals" entry in `unavailable`
            # below); a session using >1 model reports its day bucket under
            # a synthetic "mixed" key rather than an exact split.
            fams_seen = list(st.model_usage.keys())
            for day, bucket in st.day_usage.items():
                fam_key = fams_seen[0] if len(fams_seen) == 1 else "mixed"
                global_day_usage[(day, fam_key, "main")].add(_bucket_as_usage_dict(bucket))

    agents = []
    for p, source in discover_subagent_files(home):
        agent_id = os.path.basename(p).replace("agent-", "").replace(".jsonl", "").replace(".output", "")
        a = process_agent_transcript(p, since, until, source)
        if not a:
            continue
        agents.append(a)
        fam = model_family(a.model)
        global_day_usage[(day_key(a.first_ts), fam, "subagent")].add(_bucket_as_usage_dict(a.usage))
        # second pass over the same file for tool-call / Bash stats, tagged role="subagent"
        process_transcript(p, since, until, "subagent", agent_id, tool_agg,
                            bash_verb_totals, bash_subverb_totals, long_bash,
                            autobg_total, autobg_timeout_total,
                            dispatch_targets, resume_targets,
                            cpu_heavy_counter, loop_events, pipe_counter, seq_miner,
                            adoption_agg=adoption_agg, default_model_family=fam)

    tokens_by_day_model_role: dict = {}
    for (day, fam, role), bucket in global_day_usage.items():
        tokens_by_day_model_role.setdefault(day, {}).setdefault(role, {})[fam] = bucket.as_dict(fam)

    tokens_main_by_model = {fam: b.as_dict(fam) for fam, b in global_model_usage.items()}
    sub_by_model: dict = defaultdict(UsageBucket)
    for a in agents:
        merge_bucket(sub_by_model[model_family(a.model)], a.usage)
    tokens_subagent_by_model = {fam: b.as_dict(fam) for fam, b in sub_by_model.items()}

    total_cost = sum(b.cost_usd(fam) for fam, b in global_model_usage.items())
    total_cost += sum(b.cost_usd(fam) for fam, b in sub_by_model.items())

    ctx_growth = []
    compactions_total = 0
    turns_total = 0
    for st in sessions:
        compactions_total += st.compactions
        turns_total += st.turns
        if st.first_turn_ctx is not None and st.last_turn_ctx is not None and len(st.ctx_series) >= 2:
            ctx_growth.append({
                "session_id": st.session_id,
                "turns_with_usage": len(st.ctx_series),
                "first_turn_context_tokens": st.first_turn_ctx,
                "last_turn_context_tokens": st.last_turn_ctx,
                "growth_ratio": round(st.last_turn_ctx / max(st.first_turn_ctx, 1), 2),
            })

    agents_by_model = Counter(model_family(a.model) for a in agents)
    stall_total = sum(a.stalls for a in agents)
    stall_waiting_total = sum(a.stall_ending_waiting for a in agents)
    agent_durations = [
        (a.last_ts - a.first_ts).total_seconds() for a in agents if a.first_ts and a.last_ts
    ]

    wb_stats = process_wb_worklogs(wb_state_dirs, since, until, raw_dir)
    gh_stats = process_github(repo, since, until, raw_dir, offline)
    git_stats = process_git_log(git_repo_path, since, until)
    tools_section = build_tools_section(tool_agg)
    bash_section = build_bash_section(bash_verb_totals, bash_subverb_totals, long_bash,
                                       autobg_total, autobg_timeout_total, cpu_heavy_counter,
                                       loop_events, pipe_counter, seq_miner)
    codegrapher_status = check_codegrapher_status(git_repo_path)
    adoption_section = build_adoption_section(adoption_agg, codegrapher_status)
    codex_stats = process_codex_sessions(home, since, until)

    top_candidates = []
    for verb, count in bash_verb_totals.most_common():
        top_candidates.append({"pattern": f"bash:{verb}", "count": count, "kind": "tool_pattern"})
    for fam, b in global_model_usage.items():
        top_candidates.append({
            "pattern": f"main-loop tokens:{fam}",
            "estimated_tokens": b.input_tokens + b.output_tokens + b.cache_write_tokens + b.cache_read_tokens,
            "estimated_usd": round(b.cost_usd(fam), 2),
            "kind": "tokens",
        })
    for fam, b in sub_by_model.items():
        top_candidates.append({
            "pattern": f"subagent tokens:{fam}",
            "estimated_tokens": b.input_tokens + b.output_tokens + b.cache_write_tokens + b.cache_read_tokens,
            "estimated_usd": round(b.cost_usd(fam), 2),
            "kind": "tokens",
        })
    if agent_durations:
        top_candidates.append({
            "pattern": "subagent wall-clock (sum)",
            "wall_clock_hours": round(sum(agent_durations) / 3600.0, 2),
            "kind": "wall_clock",
        })
    if long_bash:
        top_candidates.append({
            "pattern": "long-running bash commands (>=30s)",
            "count": len(long_bash),
            "wall_clock_minutes": round(sum(long_bash) / 60.0, 1),
            "kind": "wall_clock",
        })
    for off in tools_section["top_result_size_offenders"][:5]:
        top_candidates.append({
            "pattern": f"tool result size:{off['role']}/{off['tool']}",
            "estimated_tokens": off["estimated_total_tokens"],
            "count": off["calls"],
            "kind": "context_bloat",
        })

    def sort_key(c):
        return ((c.get("estimated_tokens", 0) or 0)
                 + (c.get("wall_clock_minutes", 0) or 0) * 1000
                 + (c.get("wall_clock_hours", 0) or 0) * 60000
                 + (c.get("count", 0) or 0) * 10)
    top10 = sorted(top_candidates, key=sort_key, reverse=True)[:10]

    unavailable = []
    if "unavailable" in wb_stats:
        unavailable.append({"metric": "wb worklog stats", "reason": wb_stats["unavailable"]})
    else:
        unavailable.append({"metric": "wb run admission-queue wait times", "reason": wb_stats["admission_queue"].split("unavailable: ", 1)[-1]})
        unavailable.append({"metric": "wb structured refusal codes", "reason": wb_stats["refusal_codes"].split("unavailable: ", 1)[-1]})
    unavailable.append({
        "metric": "wb report stream / wb report fleet --format json",
        "reason": "no such command exists in the installed wb CLI (`wb report` is not a command group); "
                   "used ~/projects/.wb/worklogs event files directly instead",
    })
    if "unavailable" in gh_stats:
        unavailable.append({"metric": "GitHub PR/Actions data", "reason": gh_stats["unavailable"]})
    if isinstance(gh_stats.get("actions"), dict) and "unavailable" in gh_stats["actions"]:
        unavailable.append({"metric": "GitHub Actions runs", "reason": gh_stats["actions"]["unavailable"]})
    unavailable.append({
        "metric": "per-PR lint-vs-test failure rounds and review/fix rounds",
        "reason": "GitHub exposes run-level conclusion only; attributing a failed run to "
                   "lint vs test vs review requires reading job/step names or logs, which "
                   "this pass does not fetch to keep it lightweight and metrics-only",
    })
    unavailable.append({
        "metric": "wb run admission/queue history",
        "reason": "not persisted to disk by wb; only the live queue state exists, which this "
                   "extractor does not query (it may itself contend for the same CPU budget)",
    })
    unavailable.append({
        "metric": "Bash/tool wall-clock duration when no matching tool_result timestamp exists",
        "reason": "duration is approximated as (tool_result timestamp - tool_use timestamp); "
                   "calls whose tool_use was never followed by a tool_result in-window "
                   "(e.g. truncated transcripts) have no duration signal",
    })
    unavailable.append({
        "metric": "daily token totals split exactly by model when a session used more than one model",
        "reason": "SessionStats aggregates usage per model for the whole session but per-day "
                   "totals are recorded per session without a day*model cross-tab; a session "
                   "spanning >1 model in the same day is reported under a synthetic 'mixed' "
                   "model key in tokens.by_day_model_role rather than split exactly",
    })
    unavailable.append({
        "metric": "exact token count (all figures)",
        "reason": "token counts come from the harness-reported usage object (accurate); tool "
                   "RESULT sizes use a chars/4 heuristic (estimated_total_tokens), which is "
                   "directional, not the tokenizer's true count",
    })
    unavailable.append({
        "metric": "dollar cost accuracy",
        "reason": "PRICE_TABLE_USD_PER_MTOK is a hand-maintained estimate, not read from a live "
                   "pricing API; treat estimated_usd as directional, not billing-accurate",
    })
    unavailable.append({
        "metric": "batch-PR commit join (attributing a task folded into a batch PR to its "
                   "own worklog claim via final_commit)",
        "reason": "would require enumerating every window PR's commit list via a separate "
                   "paginated gh api call per PR, which this pass does not do to keep API "
                   "usage light; sealed claims and PR merges are reported separately instead",
    })
    unavailable.append({
        "metric": "CodeGrapher index status for every repo touched in the window",
        "reason": "adoption.codegrapher_index_status checks only --git-repo-path's current "
                   "state (a live snapshot, not historical); enumerating every touched repo's "
                   "index status is out of scope for this pass",
    })
    if isinstance(codex_stats, dict) and "unavailable" in codex_stats:
        unavailable.append({"metric": "Codex CLI token usage", "reason": codex_stats["unavailable"]})

    metrics = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "window": {"since": since.isoformat(), "until": until.isoformat()},
        "inputs": {
            "main_session_files": len(main_files),
            "sessions_touched_in_window": len(sessions),
            "subagent_transcript_files_considered": len(list(discover_subagent_files(home))),
            "subagents_touched_in_window": len(agents),
        },
        "tokens": {
            "by_day_model_role": tokens_by_day_model_role,
            "main_loop_by_model": tokens_main_by_model,
            "subagent_by_model": tokens_subagent_by_model,
            "estimated_total_usd": round(total_cost, 2),
        },
        "orchestrator": {
            "sessions": len(sessions),
            "turns_total": turns_total,
            "turns_per_session_median": statistics.median([s.turns for s in sessions]) if sessions else None,
            "compactions_total": compactions_total,
            "context_growth_by_session": ctx_growth,
        },
        "subagents": {
            "count_by_model": dict(agents_by_model),
            "dispatches_by_subagent_type": dict(dispatch_targets),
            "resumes_via_send_message": dict(resume_targets),
            "resume_events_total": sum(resume_targets.values()),
            "wall_clock_hours_sum": round(sum(agent_durations) / 3600.0, 2) if agent_durations else None,
            "wall_clock_hours_median": round(statistics.median(agent_durations) / 3600.0, 2) if agent_durations else None,
            "tool_uses_median": statistics.median([a.tool_uses for a in agents]) if agents else None,
            "stalls_over_5min": stall_total,
            "stalls_ending_in_waiting_for_notification_text": stall_waiting_total,
        },
        "tool_patterns": {
            "bash_by_verb": dict(bash_verb_totals),
            "auto_backgrounded_bash_count": len(autobg_total),
            "long_bash_commands_ge_30s": {
                "count": len(long_bash),
                "total_minutes": round(sum(long_bash) / 60.0, 1) if long_bash else 0.0,
            },
        },
        "tools": tools_section,
        "bash": bash_section,
        "adoption": adoption_section,
        "codex": codex_stats,
        "pipeline_and_ci": gh_stats,
        "wb_state": wb_stats,
        "git_log": git_stats,
        "top10_expensive_patterns": top10,
        "unavailable": unavailable,
        "runtime_seconds": round(time.time() - t0, 1),
    }
    return metrics


# --------------------------------------------------------------------------
# SUMMARY.md rendering
# --------------------------------------------------------------------------

def render_summary(metrics: dict, since: datetime, until: datetime) -> str:
    lines = []
    lines.append(f"# SDLC metrics summary: {since.date()} → {until.date()}")
    lines.append("")
    lines.append(f"Generated {metrics['generated_at']}. Runtime {metrics['runtime_seconds']}s.")
    lines.append("")
    lines.append("## Top 10 most expensive patterns (estimated tokens / wall-clock)")
    lines.append("")
    lines.append("| # | Pattern | Kind | Detail |")
    lines.append("|---|---------|------|--------|")
    for i, c in enumerate(metrics["top10_expensive_patterns"], 1):
        detail_bits = []
        for k in ("count", "estimated_tokens", "estimated_usd", "wall_clock_hours", "wall_clock_minutes"):
            if k in c:
                detail_bits.append(f"{k}={c[k]}")
        lines.append(f"| {i} | {c['pattern']} | {c['kind']} | {', '.join(detail_bits)} |")
    lines.append("")

    lines.append("## Unavailable metrics (input to the logging-gap analysis)")
    lines.append("")
    for u in metrics["unavailable"]:
        lines.append(f"- **{u['metric']}**: {u['reason']}")
    lines.append("")

    lines.append("## Tokens and cost")
    lines.append("")
    lines.append(f"Estimated total USD (main loop + subagents): **${metrics['tokens']['estimated_total_usd']}**")
    lines.append("")
    lines.append("Main loop by model:")
    for fam, d in metrics["tokens"]["main_loop_by_model"].items():
        lines.append(f"- {fam}: in={d['input_tokens']:,} out={d['output_tokens']:,} "
                      f"cache_w={d['cache_write_tokens']:,} cache_r={d['cache_read_tokens']:,} "
                      f"(${d['estimated_usd']})")
    lines.append("")
    lines.append("Subagents by model:")
    for fam, d in metrics["tokens"]["subagent_by_model"].items():
        lines.append(f"- {fam}: in={d['input_tokens']:,} out={d['output_tokens']:,} "
                      f"cache_w={d['cache_write_tokens']:,} cache_r={d['cache_read_tokens']:,} "
                      f"(${d['estimated_usd']})")
    lines.append("")

    lines.append("## Orchestrator")
    lines.append("")
    o = metrics["orchestrator"]
    lines.append(f"- sessions touched in window: {o['sessions']}")
    lines.append(f"- total turns: {o['turns_total']} (median per session: {o['turns_per_session_median']})")
    lines.append(f"- compactions: {o['compactions_total']}")
    if o["context_growth_by_session"]:
        ratios = [c["growth_ratio"] for c in o["context_growth_by_session"]]
        lines.append(f"- context growth ratio (last-turn / first-turn context tokens), median: "
                      f"{statistics.median(ratios):.2f}x over {len(ratios)} sessions with >=2 measured turns")
    lines.append("")

    lines.append("## Subagents")
    lines.append("")
    s = metrics["subagents"]
    lines.append(f"- count by model: {s['count_by_model']}")
    lines.append(f"- dispatches by subagent type: {s['dispatches_by_subagent_type']}")
    lines.append(f"- resumes via SendMessage: {s['resume_events_total']}")
    lines.append(f"- wall-clock hours (sum / median): {s['wall_clock_hours_sum']} / {s['wall_clock_hours_median']}")
    lines.append(f"- stalls (>5min gap while open): {s['stalls_over_5min']}, of which "
                  f"{s['stalls_ending_in_waiting_for_notification_text']} ended a turn with "
                  f'"waiting for notification" text')
    lines.append("")

    lines.append("## Tool calls (all tools)")
    lines.append("")
    lines.append("Top tool-result size offenders (role/tool, total estimated tokens):")
    lines.append("")
    lines.append("| Role | Model | Tool | Calls | Total chars | Est. tokens | Max chars |")
    lines.append("|------|-------|------|-------|--------------|-------------|-----------|")
    for off in metrics["tools"]["top_result_size_offenders"][:10]:
        lines.append(f"| {off['role']} | {off['model']} | {off['tool']} | {off['calls']} | "
                      f"{off['total_chars']:,} | {off['estimated_total_tokens']:,} | {off['max_chars']:,} |")
    lines.append("")
    rep = metrics["tools"]["repeated_identical_calls"]
    lines.append(f"Repeated identical calls: {rep['distinct_repeated_signatures']} distinct signatures repeated, "
                  f"{rep['extra_calls_beyond_first']} calls beyond the first (candidates for caching/memoization).")
    lines.append("")

    lines.append("## Bash, in depth")
    lines.append("")
    b = metrics["bash"]
    lines.append("By subverb (top 20):")
    for sv, c in list(b["by_subverb"].items())[:20]:
        lines.append(f"- `{sv}`: {c}")
    lines.append("")
    ch = b["cpu_heavy_commands"]
    lines.append(f"- CPU-heavy commands (go test/build/vet, golangci-lint, npm/pnpm/bun build/test): "
                  f"{ch['total']}, not wrapped in `wb run`: {ch['not_wrapped_in_wb_run']} "
                  f"(wb run coverage: {ch['wb_run_coverage']})")
    hl = b["hand_rolled_loops"]
    lines.append(f"- hand-rolled loops (until/while+sleep, kill -0, gh polling): {hl['count']}, "
                  f"total {hl['total_minutes']} minutes across {hl['duration_samples']} timed samples")
    pt = b["pipe_truncation"]
    lines.append(f"- piped output: bounded via tail/head: {pt['bounded_via_tail_or_head']}, unbounded: {pt['unbounded']}")
    lines.append(f"- run_in_background flag used: {b['run_in_background_flag_count']}, "
                  f"harness auto-backgrounded (foreground timeout exceeded): {b['harness_autobackgrounded_count']}")
    lines.append("")
    lines.append("Top repeated multi-call Bash sequences (candidates for one WB verb):")
    for sq in b["top_multi_call_sequences"][:10]:
        lines.append(f"- {' -> '.join(sq['sequence'])}  (x{sq['count']})")
    lines.append("")

    lines.append("## SpecScore / CodeGrapher adoption")
    lines.append("")
    ad = metrics["adoption"]
    direct = ad["direct_use"]
    lines.append("Direct CLI calls (role/model/tool -> subcommand counts):")
    any_direct = False
    for role, by_fam in direct["cli_calls_by_role_model_tool"].items():
        for fam, by_tool in by_fam.items():
            for tool, d in by_tool.items():
                any_direct = True
                subs = d.get("subcommands", {})
                total_calls = sum(subs.values())
                lines.append(f"- {role}/{fam}/{tool}: {total_calls} calls, subcommands={subs}")
    if not any_direct:
        lines.append("- (none observed in window)")
    skills = direct["skill_invocations_by_role_model_tool"]
    lines.append(f"- skill invocations: {skills if skills else '(none observed)'}")
    mcp = direct["mcp_calls_by_role_model_tool"]
    lines.append(f"- codegrapher MCP calls: {mcp if mcp else '(none observed)'}")
    lines.append("")
    mo = ad["missed_opportunities"]
    lines.append("Probable substitutes (generic tools used where a CLI query might answer directly):")
    lines.append(f"- code-structure grep by class: {mo['code_structure_grep_by_role_model_class']}")
    lines.append(f"- grep-to-read chains detected: {mo['grep_to_read_chains_by_role_model']}, "
                  f"calls plausibly saved: {mo['calls_plausibly_saved_by_role_model']}")
    lines.append(f"- spec-path lookups by class: {mo['spec_lookup_by_role_model_class']}")
    lines.append(f"- spec hunts via git log/grep: {mo['spec_hunt_via_git_or_grep_by_role_model']}")
    lines.append("")
    cs = ad["codegrapher_index_status"]
    if "unavailable" in cs:
        lines.append(f"- CodeGrapher index status: unavailable: {cs['unavailable']}")
    else:
        lines.append(f"- CodeGrapher index status (current, checked {cs['checked_at']}): "
                      f"indexed_now={cs['indexed_now']} for {cs['checked_repo']}")
    lines.append("")

    lines.append("## Codex CLI (separate harness)")
    lines.append("")
    cx = metrics["codex"]
    if "unavailable" in cx:
        lines.append(f"- unavailable: {cx['unavailable']}")
    else:
        lines.append(f"- sessions touched: {cx['sessions_touched_in_window']}, "
                      f"input={cx['input_tokens']:,}, cached_input={cx['cached_input_tokens']:,}, "
                      f"output={cx['output_tokens']:,}, reasoning_output={cx['reasoning_output_tokens']:,}")
    lines.append("")

    lines.append("## Pipeline and CI (sneat-dev/wb)")
    lines.append("")
    p = metrics["pipeline_and_ci"]
    if "unavailable" in p:
        lines.append(f"- unavailable: {p['unavailable']}")
    else:
        prs = p.get("prs", {})
        lines.append(f"- PRs created in window: {prs.get('created_in_window')}, merged: {prs.get('merged')}, "
                      f"closed unmerged: {prs.get('closed_unmerged')}, still open: {prs.get('still_open')}")
        ttm = prs.get("time_to_merge_hours", {})
        lines.append(f"- time to merge (hours): median={ttm.get('median')}, p90={ttm.get('p90')}, n={ttm.get('count')}")
        a = p.get("actions", {})
        if "unavailable" in a:
            lines.append(f"- actions runs: unavailable: {a['unavailable']}")
        else:
            lines.append(f"- Actions runs in window: {a.get('runs_in_window')}, cancelled={a.get('cancelled')}, "
                          f"failed={a.get('failed')}, wasted_minutes={a.get('wasted_minutes_cancelled_plus_failed')}")
            for wf, d in sorted(a.get("by_workflow", {}).items()):
                lines.append(f"  - {wf}: {d['runs']} runs, median {d['median_minutes']}min, p90 {d['p90_minutes']}min")
    lines.append("")

    lines.append("## WB state")
    lines.append("")
    w = metrics["wb_state"]
    if "unavailable" in w:
        lines.append(f"- unavailable: {w['unavailable']}")
    else:
        lines.append(f"- state dirs scanned: {w.get('state_dirs_scanned')}")
        lines.append(f"- worktrees claimed (all-time on disk): {w.get('claimed')}, sealed in window: {w.get('sealed')}")
        lines.append(f"- disposition breakdown: {w.get('disposition')}")
        if "landing_duration_median_s" in w:
            lines.append(f"- landing duration (claim->seal), median: {w['landing_duration_median_s']:.0f}s, "
                          f"p90: {w.get('landing_duration_p90_s', 0):.0f}s")
        he = w.get("hook_events", {})
        if "unavailable" in he:
            lines.append(f"- git-hook events: unavailable: {he['unavailable']}")
        else:
            lines.append(f"- git-hook events in window: {he.get('events_in_window')} "
                          f"(by outcome: {he.get('by_outcome_in_window')})")
        rev = w.get("surviving_run_events", {})
        if "unavailable" in rev:
            lines.append(f"- surviving `wb run` events: unavailable: {rev['unavailable']}")
        else:
            lines.append(f"- surviving `wb run` events in window: {rev.get('events_in_window')} "
                          f"from {rev.get('surviving_files')} worktrees still on disk "
                          f"(lower bound; by state: {rev.get('by_state_in_window')})")
    lines.append("")

    lines.append("## git log (sneat-dev/wb)")
    lines.append("")
    g = metrics["git_log"]
    if "unavailable" in g:
        lines.append(f"- unavailable: {g['unavailable']}")
    else:
        lines.append(f"- commits: {g['commits']}, merge commits: {g['merge_commits']}, PR references: {g['pr_references']}")
    lines.append("")

    return "\n".join(lines)


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def main(argv: Optional[list] = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--since", required=True, help="YYYY-MM-DD, inclusive, UTC start of day")
    ap.add_argument("--until", required=True, help="YYYY-MM-DD, inclusive, UTC end of day")
    ap.add_argument("--out", required=True, help="output directory for metrics.json / SUMMARY.md / raw/")
    ap.add_argument("--home", default=os.environ.get("SDLC_METRICS_HOME", str(Path.home())),
                     help="home directory to scan under (~/.claude, ~/projects/.wb); default: $HOME")
    ap.add_argument("--repo", default=os.environ.get("SDLC_METRICS_REPO", "sneat-dev/wb"),
                     help="owner/repo for GitHub PR and Actions data")
    ap.add_argument("--git-repo-path", default=os.environ.get("SDLC_METRICS_GIT_REPO_PATH", ""),
                     help="local clone path for `git log` counts; default: <home>/projects/<repo>")
    ap.add_argument("--wb-state-dir", action="append", default=None,
                     help="WB state directory (contains worklogs/, waits/); repeatable. "
                          "Default: both <home>/.wb and <home>/projects/.wb (some fleets use either "
                          "or both over time; both are scanned and de-duplicated)")
    ap.add_argument("--price-config", default=None,
                     help="path to a JSON price-table override; default: "
                          "$SDLC_METRICS_PRICE_CONFIG or ~/.config/sdlc-metrics/prices.json if present")
    ap.add_argument("--offline", action="store_true", help="reuse cached raw/ API responses instead of calling gh")
    args = ap.parse_args(argv)

    load_price_overrides(Path(args.price_config) if args.price_config else None)

    home = Path(args.home)
    since = datetime.fromisoformat(args.since).replace(tzinfo=timezone.utc)
    until = (datetime.fromisoformat(args.until) + timedelta(days=1) - timedelta(seconds=1)).replace(tzinfo=timezone.utc)
    out_dir = Path(args.out)
    git_repo_path = Path(args.git_repo_path) if args.git_repo_path else home / "projects" / args.repo
    if args.wb_state_dir:
        wb_state_dirs = [Path(p) for p in args.wb_state_dir]
    else:
        wb_state_dirs = [home / ".wb", home / "projects" / ".wb"]

    metrics = run_extract(since, until, out_dir, home, args.repo, git_repo_path, wb_state_dirs, args.offline)

    (out_dir / "metrics.json").write_text(json.dumps(metrics, indent=2, sort_keys=True), encoding="utf-8")
    (out_dir / "SUMMARY.md").write_text(render_summary(metrics, since, until), encoding="utf-8")

    eprint(f"wrote {out_dir / 'metrics.json'}")
    eprint(f"wrote {out_dir / 'SUMMARY.md'}")
    eprint(f"runtime: {metrics['runtime_seconds']}s")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
