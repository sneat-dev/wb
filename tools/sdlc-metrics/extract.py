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
import shlex
import statistics
import subprocess
import sys
import time
from collections import Counter, defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Callable, Iterable, Iterator, Optional

# --------------------------------------------------------------------------
# Price table (USD per million tokens). EDIT HERE as pricing changes.
#
# B3 fix: priced by MODEL-ID PREFIX, most specific first (e.g.
# "claude-fable-5-1" before "claude-fable-5"), falling back to a coarse
# family bucket only for a model string that matches no known prefix.
# Cache write: 1.25x input for the 5-minute ephemeral cache, 2x input for
# the 1-hour one. Cache read: 0.1x input, except Fable 5.1 which is priced
# at a flat $0.25/MTok (0.025x its $10 input rate).
#
# Source: publicly posted list prices as of the date below; re-check before
# trusting absolute dollar figures -- PRICES AS OF 2026-09-18.
# --------------------------------------------------------------------------
PRICE_TABLE_AS_OF = "2026-09-18"


def _rates(input_usd: float, output_usd: float, cache_read_usd: Optional[float] = None) -> dict:
    """Derive the full per-MTok rate dict from input/output list prices,
    using the standard cache-write multipliers (1.25x / 2x input) and the
    standard 0.1x cache-read discount unless an explicit override is given
    (Fable 5.1's flat $0.25/MTok cache read)."""
    cache_write_5m = round(input_usd * 1.25, 6)
    cache_write_1h = round(input_usd * 2.00, 6)
    cache_read = cache_read_usd if cache_read_usd is not None else round(input_usd * 0.10, 6)
    return {
        "input": input_usd,
        "output": output_usd,
        "cache_write_5m": cache_write_5m,
        "cache_write_1h": cache_write_1h,
        "cache_write": cache_write_5m,  # equal-to-5m fallback for old callers
        "cache_read": cache_read,
    }


# Most-specific-prefix-first model pricing. Matched against the lower-cased
# raw `message.model` string with str.startswith(); LEGACY_PRICE_PREFIXES
# below are labelled "legacy, verify" wherever they are surfaced.
PRICE_TABLE_BY_PREFIX: list = [
    ("claude-opus-5", _rates(5.00, 25.00)),
    ("claude-sonnet-5", _rates(2.00, 10.00)),
    ("claude-haiku-4-5", _rates(1.00, 5.00)),
    # Fable 5.1 cache read: $0.25/MTok per the Claude API pricing reference
    # as read on 2026-09-18 (differs from the usual 0.1x rule; verify when
    # updating prices).
    ("claude-fable-5-1", _rates(10.00, 50.00, cache_read_usd=0.25)),
    ("claude-fable-5", _rates(10.00, 50.00)),
    # older generations -- kept as an explicit fallback so an old transcript
    # still prices sanely; labelled "legacy, verify" wherever surfaced.
    ("claude-sonnet-4", _rates(3.00, 15.00)),
    ("claude-opus-4", _rates(15.00, 75.00)),
]
LEGACY_PRICE_PREFIXES = {"claude-sonnet-4", "claude-opus-4"}

# Coarse family fallback (current-generation rates), used only when a model
# string matches no PRICE_TABLE_BY_PREFIX entry at all -- e.g. a genuinely
# unknown or future model name. model_family() (below) does the coarse
# opus/sonnet/haiku/fable/other bucketing used here and for reporting.
PRICE_TABLE_USD_PER_MTOK = {
    "opus": _rates(5.00, 25.00),
    "sonnet": _rates(2.00, 10.00),
    "haiku": _rates(1.00, 5.00),
    "fable": _rates(10.00, 50.00),
}
DEFAULT_FAMILY = "sonnet"  # fallback price bucket for an unrecognised model string


def price_rates_for_model(model: Optional[str]) -> dict:
    """The rate dict to use for ONE message's own model string (B3/M3):
    longest-matching PRICE_TABLE_BY_PREFIX entry first, else the coarse
    family fallback, else the DEFAULT_FAMILY rates for no/unknown model."""
    if model:
        m = model.lower()
        for prefix, rates in sorted(PRICE_TABLE_BY_PREFIX, key=lambda kv: -len(kv[0])):
            if m.startswith(prefix):
                return rates
    fam = model_family(model)
    return PRICE_TABLE_USD_PER_MTOK.get(fam, PRICE_TABLE_USD_PER_MTOK[DEFAULT_FAMILY])

# Optional local price-table override, e.g. ~/.config/sdlc-metrics/prices.json
# ({"sonnet": {"input": ..., ...}, ...}) or a path given with --price-config.
# This repository is public: no machine-specific override lives in it, only
# this mechanism to load one from outside it.
_PRICE_CONFIG_ENV = "SDLC_METRICS_PRICE_CONFIG"


def _apply_rate_override(existing: dict, override: dict) -> dict:
    """Merge a user override into one existing per-model/family rate dict.
    minor #5: overriding just `input` must not leave cache_write_5m/1h
    pointing at the OLD input price -- recompute those DERIVED rates from
    the (possibly new) input, unless the override itself gives them
    explicitly. `cache_read` is left untouched unless the override names
    it: a model's non-standard cache-read pricing (e.g. Fable 5.1's flat
    $0.25/MTok) must never be silently reset to the 0.1x default just
    because `input` changed."""
    merged = dict(existing)
    input_usd = override.get("input", existing.get("input")) or 0.0
    output_usd = override.get("output", existing.get("output")) or 0.0
    merged["input"] = input_usd
    merged["output"] = output_usd
    if "cache_write_5m" not in override:
        merged["cache_write_5m"] = round(input_usd * 1.25, 6)
    if "cache_write_1h" not in override:
        merged["cache_write_1h"] = round(input_usd * 2.00, 6)
    for k, v in override.items():
        merged[k] = v  # any explicitly given field wins outright, last
    merged["cache_write"] = merged["cache_write_5m"]  # equal-to-5m fallback for old callers
    # minor #3 (round 3): a brand-new prefix added via --price-config with
    # no `cache_read` (and no existing entry to inherit one from) used to
    # leave this field missing entirely -- UsageBucket.add() then raised
    # KeyError('cache_read') the first time that model's usage was priced.
    # Fall back to the usual 0.1x-of-input rule rather than crash.
    if "cache_read" not in merged or merged.get("cache_read") is None:
        merged["cache_read"] = round(input_usd * 0.1, 6)
    return merged


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
            for key, rates in overrides.items():
                if key in PRICE_TABLE_USD_PER_MTOK:
                    # minor #5: a coarse family override, e.g.
                    # {"sonnet": {...}}, must actually take effect for a
                    # real model string -- price_rates_for_model() always
                    # tries the more-specific PRICE_TABLE_BY_PREFIX entry
                    # FIRST, so a family-only override that stopped at
                    # PRICE_TABLE_USD_PER_MTOK would silently do nothing
                    # for "claude-sonnet-5". Apply it to the family
                    # fallback AND to every current-generation prefix
                    # entry in that family (never a "legacy, verify" one,
                    # which is deliberately priced differently).
                    PRICE_TABLE_USD_PER_MTOK[key] = _apply_rate_override(PRICE_TABLE_USD_PER_MTOK[key], rates)
                    for i, (prefix, existing) in enumerate(PRICE_TABLE_BY_PREFIX):
                        if prefix in LEGACY_PRICE_PREFIXES:
                            continue
                        if model_family(prefix) == key:
                            PRICE_TABLE_BY_PREFIX[i] = (prefix, _apply_rate_override(existing, rates))
                    continue
                # a model-id-prefix override/addition, e.g.
                # {"claude-sonnet-5-2": {...}}
                for i, (prefix, existing) in enumerate(PRICE_TABLE_BY_PREFIX):
                    if prefix == key:
                        PRICE_TABLE_BY_PREFIX[i] = (prefix, _apply_rate_override(existing, rates))
                        break
                else:
                    PRICE_TABLE_BY_PREFIX.append((key, _apply_rate_override({"input": 0.0, "output": 0.0}, rates)))
            eprint(f"price overrides loaded from {p}")
            return

STALL_THRESHOLD_SECONDS = 5 * 60
CHARS_PER_TOKEN_ESTIMATE = 4.0  # rough, English-text heuristic; see README

CPU_HEAVY_PATTERNS = [
    # kept for any caller that still wants a raw-string check; the
    # token-aware is_cpu_heavy_tokens() below is what process_transcript
    # actually uses so a quoted/heredoc mention of "go test" is never
    # mistaken for a real invocation (minor #1).
    re.compile(r"\bgo\s+(test|build|vet)\b"),
    re.compile(r"\bgolangci-lint\b"),
    re.compile(r"\b(npm|pnpm|bun|bunx|yarn)\s+(run\s+)?(build|test)\b"),
]

# CPU-heavy detection at the TOKEN level (M2, minor #1): a shlex token is
# never split across a quote boundary, so "go" immediately followed by the
# token "test" can only mean a real `go test` invocation, never a mention
# inside a quoted string or heredoc body.
_NODEISH_TOOLS = ("npm", "pnpm", "bun", "bunx", "yarn")


def is_cpu_heavy_tokens(tokens: list) -> bool:
    n = len(tokens)
    for i, t in enumerate(tokens):
        if t == "go" and i + 1 < n and tokens[i + 1] in ("test", "build", "vet"):
            return True
        if t == "golangci-lint":
            return True
        if t in _NODEISH_TOOLS:
            rest = tokens[i + 1:i + 3]
            if rest[:1] == ["run"]:
                rest = rest[1:2]
            if rest[:1] and rest[0] in ("build", "test"):
                return True
    return False


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
# minor #1: the specscore/codegrapher subcommand recorded in adoption
# stats must be one of these, or the generic "(other)" placeholder --
# never an arbitrary word lifted from the command line (a quoted
# free-text argument, once its quoting is lost by rejoining tokens into a
# string, looks exactly like a second bareword and would otherwise leak
# through, e.g. specscore "launch secret project" -> subcommand "launch").
SPECSCORE_SUBCOMMANDS = {
    "install", "self-update", "change-status", "code", "feature", "idea",
    "plan", "spec", "task", "rule",
}
CODEGRAPHER_SUBCOMMANDS = {
    "index", "status", "callers", "callees", "symbol", "symbols", "trace",
    "search", "query", "graph", "refs", "definition", "impact",
}

# B4: a leading verb (the LEADING command word of a stripped segment) is
# only ever surfaced verbatim in metrics.json/SUMMARY.md if it is a known
# dev-tool name. Anything else -- including a word that leaked out of a
# quoted string despite the shlex-based tokenising below, or a genuinely
# unknown/adversarial command -- becomes the generic "<other>" label.
KNOWN_COMMAND_ALLOWLIST = {
    "git", "gh", "go", "wb", "specscore", "codegrapher", "grep", "rg", "find",
    "cat", "sed", "ls", "python3", "python", "node", "pnpm", "npm", "bun",
    "bunx", "yarn", "make", "jq", "curl", "wget", "echo", "mkdir", "rm", "cp",
    "mv", "touch", "chmod", "diff", "tail", "head", "wc", "xargs", "tar",
    "docker", "kubectl", "pip", "pip3", "golangci-lint", "printf", "true",
    "false", "test", "sort", "uniq", "awk", "tr", "basename", "dirname",
    "which", "pwd", "date", "sleep", "kill", "less", "more", "gzip", "gunzip",
    "tee", "ssh", "scp", "rsync", "eslint", "prettier", "tsc", "cargo",
    "rustc", "brew", "codegrapher", "gofmt", "goimports",
}

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

# minor #2: tighten "symbol-like" so it means identifier-shaped, not a file
# name -- "README.md" or "main.go" otherwise matches the ".Foo" member-
# access pattern above (the extension looks like a member) and gets
# misclassified as a code-structure lookup.
FILENAME_LIKE_RE = re.compile(
    r"^[\w-]+\.(go|ts|tsx|js|jsx|py|java|rb|rs|c|cc|cpp|h|hpp|cs|kt|swift|"
    r"md|txt|json|ya?ml|toml|cfg|ini|log|csv|sh|env)$",
    re.IGNORECASE,
)


def classify_grep_pattern(pattern: str) -> str:
    """Classify a grep/rg/Grep-tool pattern as 'symbol-like' (looks like a
    code-structure lookup a CodeGrapher query would answer directly),
    'regex' (a real regular expression, not just an identifier), or 'text'
    (free text search). Returns only the class; the pattern itself must
    never be logged by the caller."""
    p = (pattern or "").strip()
    if not p:
        return "text"
    if FILENAME_LIKE_RE.match(p):
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


# flags that take a following value argument, for grep/rg -- without this a
# flag's VALUE (e.g. the "go" in `rg -t go X`) is mistaken for the pattern
# (minor #2).
_GREP_FLAGS_WITH_ARG = {
    "-t", "--type", "-T", "--type-not", "-g", "--glob", "-m", "--max-count",
    "-A", "--after-context", "-B", "--before-context", "-C", "--context",
    "-e", "--regexp", "-f", "--file", "--include", "--exclude",
    "--iglob", "--max-depth", "-M", "--max-columns",
}


def extract_bash_grep_pattern(cmd: str) -> Optional[str]:
    """Best-effort extraction of the search pattern from a grep/rg/git-grep
    Bash invocation, for classification only -- never returned to a caller
    that might log it verbatim. Quote-safe (shlex-tokenised) and skips a
    flag's value argument (e.g. `rg -t go X` -> "X", not "go")."""
    toks = tokenize_command(cmd)
    if toks is None:
        toks = cmd.strip().split()
    if not toks:
        return None
    if toks[0] == "git" and len(toks) > 1 and toks[1] == "grep":
        i = 2
    elif toks[0] in ("rg", "grep"):
        i = 1
    else:
        return None
    while i < len(toks):
        t = toks[i]
        if t == "--":
            i += 1
            continue
        if t in _GREP_FLAGS_WITH_ARG:
            i += 2
            continue
        if t.startswith("-") and t != "-":
            i += 1
            continue
        return t.strip("'\"")
    return None


def extract_bash_read_path(cmd: str) -> Optional[str]:
    """Best-effort path argument for cat/head/tail/sed -n <path> reads.
    Quote-safe (shlex-tokenised)."""
    toks = tokenize_command(cmd)
    if toks is None:
        toks = cmd.strip().split()
    if not toks:
        return None
    if toks[0] in ("cat", "head", "tail"):
        rest = [t for t in toks[1:] if not t.startswith("-")]
        return rest[0] if rest else None
    if toks[0] == "sed" and len(toks) > 1 and toks[1] == "-n":
        rest = [t for t in toks[2:] if not t.startswith("-") and not re.match(r"^[\d,$p]+$", t)]
        return rest[-1] if rest else None
    return None


def eprint(*a: Any, **kw: Any) -> None:
    print(*a, file=sys.stderr, **kw)


# M4: every path this pass writes into metrics.json/SUMMARY.md (repo
# checkout paths, WB state dirs, "unavailable" reasons that mention a
# scanned path, ...) is rendered relative to home ("~/...") instead of the
# raw OS username -- never /home/<user>/... or /Users/<user>/... verbatim.
_HOME_PATH_RE = re.compile(r"/(?:home|Users)/[^/\s]+/")


def scrub_home_path(s: Any) -> Any:
    """Render an absolute path (or any string that may contain one) with
    its home-directory prefix replaced by "~/" (M4). Non-strings pass
    through unchanged; a list/tuple of paths is scrubbed element-wise."""
    if isinstance(s, (list, tuple)):
        return [scrub_home_path(x) for x in s]
    if not isinstance(s, str):
        return s
    return _HOME_PATH_RE.sub("~/", s)


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


HEREDOC_START_RE = re.compile(r"<<-?\s*['\"]?(\w+)['\"]?")


def strip_heredocs(cmd: str) -> str:
    """Remove heredoc BODIES (the lines between a `<<[-]DELIM` marker and the
    line containing just DELIM) before any tokenising or classification --
    their contents are never inspected for verb/pattern extraction (B4).
    The marker line itself is kept so the surrounding command structure is
    still visible and still classifies (e.g. the `cat` in `cat > f <<EOF`).
    minor #1 (round 3): the closing delimiter line is now DROPPED, not
    kept -- with NM1's newline-to-`;` rewrite, keeping it turned the bare
    delimiter word into its own fake command position (`EOF` showed up
    1,346 times in `by_verb` on real data). The line after it still gets
    its own newline separator from the marker line, so a following real
    command is still classified on its own."""
    if "<<" not in cmd:
        return cmd
    lines = cmd.split("\n")
    out = []
    i = 0
    while i < len(lines):
        line = lines[i]
        out.append(line)
        m = HEREDOC_START_RE.search(line)
        if m:
            delim = m.group(1)
            i += 1
            while i < len(lines) and lines[i].strip() != delim:
                i += 1
            if i < len(lines):
                i += 1  # skip the closing delimiter line -- never classified
            continue
        i += 1
    return "\n".join(out)


def _replace_top_level_newlines_with_semicolons(cmd: str) -> str:
    """NM1: shlex's default whitespace includes '\\n', so a bare newline is
    silently swallowed as ordinary whitespace between tokens rather than
    ending one command and starting the next -- e.g. a heredoc's closing
    delimiter line followed on the next physical line by a real command
    (`cat > f <<'EOF' ... EOF\\ngo test ./...`) tokenises as ONE run-on
    segment and the second line's verb is lost. Rewrite every newline that
    is NOT inside an open quote into a literal `;` clause separator before
    tokenising. A newline that IS inside an open quote is left alone --
    it's part of that string's content, which shlex already handles."""
    out = []
    in_squote = False
    in_dquote = False
    escape = False
    for ch in cmd:
        if escape:
            out.append(ch)
            escape = False
            continue
        if ch == "\\" and not in_squote:
            out.append(ch)
            escape = True
            continue
        if ch == "'" and not in_dquote:
            in_squote = not in_squote
            out.append(ch)
            continue
        if ch == '"' and not in_squote:
            in_dquote = not in_dquote
            out.append(ch)
            continue
        if ch == "\n" and not in_squote and not in_dquote:
            out.append(";")
            continue
        out.append(ch)
    return "".join(out)


def tokenize_command(cmd: str) -> Optional[list]:
    """Quote-safe shell tokenisation (B4): shlex with punctuation_chars so
    control operators (&&, ||, ;, |, &) come back as their own tokens and a
    quoted string -- including one that starts a VAR='...' assignment --
    stays a single token, never split into bare words. Top-level newlines
    are rewritten to `;` first (NM1) so a multi-line Bash call classifies
    every physical line, not just the first. Returns None (callers must
    emit "<unparsed>") if the command cannot be tokenised at all, e.g.
    unbalanced quotes."""
    try:
        cmd = _replace_top_level_newlines_with_semicolons(strip_heredocs(cmd))
        lex = shlex.shlex(cmd, posix=True, punctuation_chars=True)
        lex.whitespace_split = True
        # minor #2: shlex's default commenters='#' truncates the command at
        # the first bare '#', which real commands carry mid-word all the
        # time (an issue number like `owner/repo#583`, a URL fragment).
        # This tool never needs shell-comment stripping, so disable it.
        lex.commenters = ""
        return list(lex)
    except ValueError:
        return None


_SHELL_OPERATORS = {"&&", "||", ";", "|", "&"}
_CLAUSE_OPERATORS = {"&&", "||", ";"}
_NOISE_PREFIX_HEADS = {"cd", "timeout", "env", "nice", "sudo"}


def split_command_segments_with_ops(cmd: str, operators: Optional[set] = None) -> list:
    """Split a command line into its top-level segments (default: on &&,
    ||, ;, |, & -- M2), quote-safe. Returns a list of (preceding_op, token
    list) pairs; `preceding_op` is None for the very first segment. []
    if the command could not be tokenised."""
    toks = tokenize_command(cmd)
    if toks is None:
        return []
    ops = operators if operators is not None else _SHELL_OPERATORS
    segments, current = [], []
    preceding_op = None
    for t in toks:
        if t in ops:
            if current:
                segments.append((preceding_op, current))
            current = []
            preceding_op = t
        else:
            current.append(t)
    if current:
        segments.append((preceding_op, current))
    return segments


def split_command_segments(cmd: str, operators: Optional[set] = None) -> list:
    """Split a command line into its top-level segments, quote-safe.
    Returns a list of token lists only -- see split_command_segments_with_ops
    for the preceding-operator info (NM2: needed to tell a pipe-downstream
    filter apart from a genuine new command position)."""
    return [toks for _op, toks in split_command_segments_with_ops(cmd, operators)]


#  NM1: `timeout -k <seconds> <duration> cmd` and `timeout --signal=SIG
#  <duration> cmd` both need their KILL/SIGNAL option's own value token
#  consumed too, when it's given as a separate token rather than glued
#  with '=' -- otherwise that value (a bare number) is mistaken for the
#  command's own leading verb. Long forms glued with '=' (`--kill-after=5`)
#  are already a single token and need no special-casing.
_TIMEOUT_OPTS_WITH_SEPARATE_ARG = {"-k", "--kill-after", "-s", "--signal"}
#  Likewise `nice -n <adjustment> cmd` -- but the traditional single-token
#  form `nice -<adjustment> cmd` (e.g. `nice -19 cmd`) is already handled
#  generically since it's one token starting with '-'.
_NICE_OPTS_WITH_SEPARATE_ARG = {"-n", "--adjustment"}


def strip_segment_prefix(tokens: list) -> list:
    """Drop leading `cd <dir>`, `timeout [flags] N`, `env [VAR=..]* [-i]`,
    `nice [-n N]`, `sudo`, and VAR=value assignment tokens from one segment,
    to find the real command it runs (M2: a leading `cd X &&` must not break
    classification of the command that follows). NM1: an option that takes
    its value as a SEPARATE token (`timeout -k 5`, `nice -n 19`) drops that
    value too, so it never gets mistaken for the real command's verb."""
    toks = list(tokens)
    changed = True
    while toks and changed:
        changed = False
        while toks and ENV_ASSIGNMENT_RE.match(toks[0]):
            toks = toks[1:]
            changed = True
        if not toks:
            break
        head = toks[0]
        if head == "cd":
            toks = toks[2:] if len(toks) > 1 else []
            changed = True
        elif head == "timeout":
            toks = toks[1:]
            while toks and toks[0].startswith("-"):
                opt = toks[0]
                toks = toks[1:]
                if opt in _TIMEOUT_OPTS_WITH_SEPARATE_ARG and toks:
                    toks = toks[1:]
            if toks:
                toks = toks[1:]  # the duration argument
            changed = True
        elif head == "env":
            toks = toks[1:]
            while toks and (ENV_ASSIGNMENT_RE.match(toks[0]) or toks[0].startswith("-")):
                toks = toks[1:]
            changed = True
        elif head == "nice":
            toks = toks[1:]
            while toks and toks[0].startswith("-"):
                opt = toks[0]
                toks = toks[1:]
                if opt in _NICE_OPTS_WITH_SEPARATE_ARG and toks:
                    toks = toks[1:]
            changed = True
        elif head == "sudo":
            toks = toks[1:]
            changed = True
    return toks


ENV_ASSIGNMENT_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
SAFE_VERB_RE = re.compile(r"^[A-Za-z][A-Za-z0-9_.\-]*$")

# minor #1 (round 3): shell keywords are control-flow syntax, never a
# command of their own, so a segment beginning with one (a stray `do`/
# `done`/`for` from a for-loop, `(`/`)` from a subshell, `then`/`fi` from
# an if) must never occupy a command position.
_SHELL_KEYWORDS = {
    "do", "done", "for", "then", "fi", "else", "elif", "while", "case",
    "esac", "in", "function", "select", "until", "time",
}
_LONE_PUNCT_TOKENS = {"(", ")", "{", "}", "[", "]", "[[", "]]"}


def _is_non_command_leading_token(tok: str) -> bool:
    """True if `tok` cannot possibly be a command's own leading verb --
    shell keyword, lone punctuation, or a bare digit (a redirection
    file-descriptor number stranded after prefix-stripping, e.g. the `2`
    in `cd dir 2>/dev/null` once `cd dir` is stripped)."""
    if tok in _SHELL_KEYWORDS or tok in _LONE_PUNCT_TOKENS:
        return True
    if tok.isdigit():
        return True
    return False


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


# minor #1: subverb tables must never echo an arbitrary second/third
# token verbatim -- a package name, branch name or free-text argument
# would leak straight into by_subverb otherwise (e.g. `bunx
# private-pkg-name` showing up as subverb "bunx private-pkg-name"). Only a
# token in one of these known-subcommand sets is ever surfaced; anything
# else becomes the generic "(other)" placeholder.
GIT_SUBCOMMANDS = {
    "status", "push", "pull", "fetch", "merge", "rebase", "log", "diff",
    "commit", "add", "checkout", "branch", "stash", "clone", "init", "tag",
    "show", "reset", "cherry-pick", "remote", "config", "grep", "blame",
    "worktree", "rev-parse", "describe", "ls-files", "submodule", "mv", "rm",
    "switch", "restore", "apply", "am", "bisect", "reflog",
}
GH_SUBCOMMANDS = {
    "api", "run", "pr", "issue", "repo", "workflow", "release", "auth", "ci",
    "gist", "label", "search", "browse", "secret", "variable", "cache",
    "attestation", "ruleset", "project", "org", "codespace", "extension",
    "alias", "completion", "status", "config",
}
WB_SUBCOMMANDS = {
    "pr", "worktree", "run", "wait", "deps", "agent", "stream", "branch",
    "sync", "status", "land", "create", "install", "upgrade", "self-update",
    "hooks", "fleet", "daemon", "coverage", "report", "check", "verify",
    "skills", "migrate", "config", "version",
}
NODE_PKG_SUBCOMMANDS = {
    "install", "i", "add", "remove", "rm", "run", "build", "test", "exec",
    "dlx", "start", "ci", "update", "up", "outdated", "list", "ls", "why",
    "publish", "pack", "link", "unlink", "create", "init", "x", "audit",
    "prune",
}


def _allowlisted_sub(tool: str, sub: str) -> str:
    allowlist = {
        "git": GIT_SUBCOMMANDS, "gh": GH_SUBCOMMANDS, "wb": WB_SUBCOMMANDS,
        "npm": NODE_PKG_SUBCOMMANDS, "pnpm": NODE_PKG_SUBCOMMANDS,
        "bun": NODE_PKG_SUBCOMMANDS, "bunx": NODE_PKG_SUBCOMMANDS,
        "yarn": NODE_PKG_SUBCOMMANDS,
    }.get(tool)
    if allowlist is None or sub in allowlist:
        return sub
    return "(other)"


def classify_segment_tokens(toks: list) -> tuple:
    """(verb, subverb) for one already-split, prefix-stripped segment's
    tokens. B4: the LEADING verb is only ever surfaced if it is on
    KNOWN_COMMAND_ALLOWLIST -- any other bare word (including one that
    leaked out of a mis-parsed quoted string) becomes "<other>", never the
    word itself."""
    if not toks:
        return "(empty)", "(empty)"
    raw_verb = toks[0]
    if ENV_ASSIGNMENT_RE.match(raw_verb):
        return "<assignment>", "<assignment>"
    verb = _sanitize_verb(raw_verb)
    if verb in ("<other>", "<assignment>", "(empty)"):
        return verb, verb
    if verb.lower() not in KNOWN_COMMAND_ALLOWLIST:
        return "<other>", "<other>"
    if verb in SUBVERB_TOOLS and len(toks) > 1:
        raw_sub = _sanitize_verb(toks[1])
        # minor #1: allowlist the SECOND token (the real subcommand) per
        # tool family before it can ever reach the report -- a branch,
        # package, or file name typed as the second argument must never
        # leak verbatim into by_subverb. The THIRD token is never a
        # tool-level subcommand of its own (it's an argument TO the
        # second one, e.g. "list" in "gh run list") -- it only ever
        # surfaces below through the explicit two_word allowlist, so it
        # needs no separate gate here, just syntax sanitisation.
        # minor #4 (round 3): an unsafe/unparseable second token (raw_sub
        # is "<other>"/"<assignment>"/"(empty)") is ALSO an unknown
        # subcommand -- route it through the same "(other)" placeholder
        # the allowlist itself uses, instead of leaking a second, distinct
        # placeholder spelling ("git <other>" alongside "git (other)")
        # for what is really the same "no known subcommand" case.
        sub = _allowlisted_sub(verb, raw_sub) if raw_sub not in ("<other>", "<assignment>", "(empty)") else "(other)"
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
    return verb, verb


def iter_command_segments(cmd: str):
    """Yield (verb, subverb, joined_segment_str, is_pipe_stage, tokens) for
    EVERY meaningful, prefix-stripped segment of a command line, split on
    &&/||/;/|/& (M2): `cd x && go test ./...` classifies its second segment
    as `go test`, not `cmd:cd`. `is_pipe_stage` is True for a segment
    that follows a `|` (a pipe FILTER downstream of the real command, e.g.
    `head`/`grep` in `git log | head`), False for one that starts a new
    command position (the first segment, or one following &&/||/;/&) --
    NM2: a pipe filter is not a new command call and must not be counted
    as one in the verb table or the sequence miner. `tokens` is the raw
    prefix-stripped token LIST (minor #1: a caller that needs the real
    second token, e.g. a CLI subcommand, must read it from here, never by
    re-splitting the joined string -- a quoted multi-word argument loses
    its quoting once joined, so `specscore "launch secret project"` would
    otherwise re-split into a fake subcommand "launch"). An unparsable
    command yields a single ("<unparsed>", "<unparsed>", "", False, [])
    segment."""
    toks = tokenize_command(cmd)
    if toks is None:
        yield "<unparsed>", "<unparsed>", "", False, []
        return
    for preceding_op, seg in split_command_segments_with_ops(cmd):
        stripped = strip_segment_prefix(seg)
        if not stripped:
            continue
        # minor #1 (round 3): a shell keyword (`do`/`done`/`for`/...), a
        # bare redirection file-descriptor number left over from something
        # like `cd dir 2>/dev/null`, or lone punctuation (`(`, `)`, `{`,
        # `}`) is shell SYNTAX, never a command of its own -- it must not
        # occupy a command position in by_verb/pipe_filters/the sequence
        # miner just because it happened to land between two operators.
        if _is_non_command_leading_token(stripped[0]):
            continue
        verb, subverb = classify_segment_tokens(stripped)
        yield verb, subverb, " ".join(stripped), preceding_op == "|", stripped


def leading_verb_and_subverb(cmd: str) -> tuple:
    """(verb, subverb) for a shell command's FIRST meaningful segment, after
    splitting on &&/||/;/|/& and dropping cd/timeout/env/nice/sudo/VAR=
    prefixes. Kept for callers that only care about one representative
    segment; iter_command_segments() classifies every segment (M2)."""
    for verb, subverb, _seg_str, _is_pipe_stage, _tokens in iter_command_segments(cmd):
        return verb, subverb
    return "(empty)", "(empty)"


def _classify_go_test_segment(tokens: list) -> str:
    """NM3: split `go test` by SCOPE instead of lumping every invocation
    under the misleading "(full)" label -- most aren't whole-module runs.
    `./...` (exactly) is a whole-MODULE run; any other `.../...` pattern
    is a subtree run; anything else (a single directory/package path, or
    no path at all) is a single-package run. `-run` is matched as an
    exact TOKEN (`-run` or `-run=...`), never a substring -- a package
    path like `./cmd/wb-runner/...` must never be mistaken for -run."""
    scope = "package"
    for t in tokens:
        if t == "./...":
            scope = "module"
            break
        if scope != "module" and t.endswith("/..."):
            scope = "subtree"
    has_run = any(t == "-run" or t.startswith("-run=") for t in tokens)
    label = {
        "module": "go test ./... (module)",
        "subtree": "go test (subtree)",
        "package": "go test (package)",
    }[scope]
    return f"{label} -run" if has_run else label


def _classify_bash_verb_segment(verb: str, subverb: str, seg_str: str,
                                 tokens: Optional[list] = None) -> str:
    """Coarse classification for one already-split, already-classified
    (verb, subverb) segment. Shared by classify_bash_verb() (first segment
    only, kept for backward compatibility) and process_transcript's
    per-segment loop (M2). `tokens` is the real token list when the caller
    has one (NM3 needs exact-token `-run` matching, not a substring scan
    of the rejoined string)."""
    if tokens is None:
        tokens = seg_str.split()
    if verb == "go" and "test" in subverb:
        return _classify_go_test_segment(tokens)
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
    if any(p.search(seg_str) for p in LOOP_PATTERNS):
        return "sleep/poll-loop"
    if verb == "git":
        return "git"
    if verb in ("<other>", "<assignment>", "(empty)", "<unparsed>"):
        return verb
    # minor #3: "other:" reads as "unknown command" -- it's the opposite,
    # an ALLOWLISTED verb with no dedicated bucket. "cmd:" says that.
    return f"cmd:{verb}" if verb.lower() in KNOWN_COMMAND_ALLOWLIST else "<other>"


def classify_bash_verb(cmd: str) -> str:
    """Backward-compatible coarse classification used for the top-level
    tool_patterns.bash_by_verb table (first segment only)."""
    for verb, subverb, seg_str, _is_pipe_stage, tokens in iter_command_segments(cmd):
        return _classify_bash_verb_segment(verb, subverb, seg_str, tokens)
    return _classify_bash_verb_segment("(empty)", "(empty)", "")


def is_cpu_heavy(cmd: str) -> bool:
    """Token-aware CPU-heavy check (minor #1): a quoted mention of "go test"
    can never match, only a real, unquoted invocation."""
    toks = tokenize_command(cmd)
    if toks is None:
        return any(p.search(cmd.strip()) for p in CPU_HEAVY_PATTERNS)
    return is_cpu_heavy_tokens(toks)


def is_wb_run_wrapped(cmd: str) -> bool:
    return bool(re.search(r"\bwb\s+run\b", cmd))


def cpu_heavy_clauses(cmd: str) -> list:
    """Yield (effective_text, wrapped_by_wb_run) for each top-level CLAUSE
    (split on &&/||/; only -- a `|` pipe stays part of the same clause, so
    `wb run -- cmd | tee log` is still recognised as wrapped). Minor #1:
    `wb run` wraps only the clause it prefixes, not siblings joined by &&."""
    out = []
    for clause in split_command_segments(cmd, operators=_CLAUSE_OPERATORS):
        stripped = strip_segment_prefix(clause)
        if not stripped:
            continue
        wrapped = False
        effective = stripped
        if stripped[0] == "wb" and len(stripped) > 1 and stripped[1] == "run":
            wrapped = True
            if "--" in stripped:
                idx = stripped.index("--")
                effective = stripped[idx + 1:]
            else:
                effective = stripped[2:]
        out.append((" ".join(effective), wrapped))
    return out


def is_hand_rolled_loop(cmd: str) -> bool:
    return any(p.search(cmd.strip()) for p in LOOP_PATTERNS) or bool(POLL_COMMAND_HINTS.search(cmd.strip()))


def has_bounded_pipe(cmd: str) -> Optional[bool]:
    """True if the command pipes through tail/head (bounded output), False
    if it has a pipe but no bound, None if there is no pipe at all."""
    if "|" not in cmd:
        return None
    segments = [s.strip() for s in cmd.split("|")]
    return any(re.match(r"^(tail|head)\b", s) for s in segments[1:])


def reduce_command(cmd: str) -> str:
    """Reduce a shell command line to verb + flags, paths collapsed.

    Privacy: heredoc bodies are stripped, no literal path segments, no
    quoted string arguments, no other free text -- only the leading verb
    and flag tokens survive.
    """
    cmd = strip_heredocs(cmd).strip()
    cmd = PATH_RE.sub("<path>", cmd)
    cmd = LONG_FLAG_VALUE_RE.sub(r"\1<val>", cmd)
    for pat in QUOTED_RE:
        cmd = pat.sub("<str>", cmd)
    tokens = cmd.split()
    return " ".join(tokens[:6])  # verb + first few flags only


def command_tokens(cmd: str) -> list:
    return cmd.strip().split()


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


_ID_SHAPED_RE = re.compile(r"^[A-Za-z0-9_-]{6,64}$")


def resume_target_label(to: Any) -> str:
    """Minor #8: SendMessage's `to` field can be an opaque agent id, or a
    free-text agent NAME the dispatcher chose (may echo task/PII-adjacent
    words) -- never store either verbatim, only a coarse kind guess
    ("id" vs "name") plus a short hash."""
    s = str(to or "")
    kind = "id" if _ID_SHAPED_RE.match(s) else "name"
    digest = hashlib.sha1(s.encode("utf-8", "replace")).hexdigest()[:10]
    return f"{kind}:{digest}"


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
    cost_usd_accum: float = 0.0  # priced at add() time, per-message's own model (B3/M3)
    cache_read_cost_usd_accum: float = 0.0  # the cache-read SLICE of cost_usd_accum, for the SUMMARY headline

    @property
    def cache_write_tokens(self) -> int:
        return self.cache_write_5m_tokens + self.cache_write_1h_tokens

    def add(self, usage: dict, model: Optional[str] = None) -> None:
        """Add one API response's usage, priced by `model` -- the message's
        OWN model string (B3/M3: never a coarse family-only price, and
        never a declared-but-possibly-"inherit" subagent model).

        B1 fix: `usage.iterations`, when present and non-empty, is already
        the full per-sub-request breakdown that the top-level usage figure
        aggregates -- summing both double-counts every token (14,657 of
        19,143 real records carry exactly one iterations[type=message]
        entry equal to the top-level usage). When iterations are present,
        sum ONLY the iterations (each priced at its own model if it names
        one, else `model`); otherwise use the top-level usage.
        """
        self.messages += 1
        iterations = [it for it in (usage.get("iterations") or []) if isinstance(it, dict)]
        if iterations:
            for it in iterations:
                self._add_raw(it, it.get("model") or model)
        else:
            self._add_raw(usage, model)

    def _add_raw(self, usage: dict, model: Optional[str]) -> None:
        """Add one usage object's raw token fields (never its `iterations`,
        which add() has already resolved) and price it by `model`."""
        input_tokens = int(usage.get("input_tokens") or 0)
        output_tokens = int(usage.get("output_tokens") or 0)
        cache_read_tokens = int(usage.get("cache_read_input_tokens") or 0)
        cc = usage.get("cache_creation")
        if isinstance(cc, dict):
            cache_write_5m = int(cc.get("ephemeral_5m_input_tokens") or 0)
            cache_write_1h = int(cc.get("ephemeral_1h_input_tokens") or 0)
        else:
            # a usage object without the 5m/1h split is assumed 1h, since
            # that is what this harness's orchestrator writes (see README)
            cache_write_5m = 0
            cache_write_1h = int(usage.get("cache_creation_input_tokens") or 0)

        self.input_tokens += input_tokens
        self.output_tokens += output_tokens
        self.cache_read_tokens += cache_read_tokens
        self.cache_write_5m_tokens += cache_write_5m
        self.cache_write_1h_tokens += cache_write_1h

        rates = price_rates_for_model(model)
        cache_read_cost = cache_read_tokens * rates["cache_read"] / 1_000_000.0
        self.cache_read_cost_usd_accum += cache_read_cost
        self.cost_usd_accum += (
            input_tokens * rates["input"]
            + output_tokens * rates["output"]
            + cache_write_5m * rates.get("cache_write_5m", rates.get("cache_write", 0))
            + cache_write_1h * rates.get("cache_write_1h", rates.get("cache_write", 0))
        ) / 1_000_000.0 + cache_read_cost

    def cost_usd(self, family: Optional[str] = None) -> float:
        """The accumulated cost, priced per-message at add() time. `family`
        is accepted only for backward compatibility with older call sites
        and is otherwise ignored -- see add()/M3."""
        return self.cost_usd_accum

    def as_dict(self, family: Optional[str] = None) -> dict:
        d = {
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "cache_write_tokens": self.cache_write_tokens,
            "cache_write_5m_tokens": self.cache_write_5m_tokens,
            "cache_write_1h_tokens": self.cache_write_1h_tokens,
            "cache_read_tokens": self.cache_read_tokens,
            "messages": self.messages,
            "estimated_usd": round(self.cost_usd_accum, 4),
            "estimated_cache_read_usd": round(self.cache_read_cost_usd_accum, 4),
        }
        return d


def merge_bucket(dst: UsageBucket, src: UsageBucket) -> None:
    """Merge src's raw tokens AND its already-computed cost into dst,
    without re-pricing -- src's tokens may span more than one message's own
    model, so only the per-message-priced cost it already accumulated is
    trustworthy (B3/M3)."""
    dst.input_tokens += src.input_tokens
    dst.output_tokens += src.output_tokens
    dst.cache_write_5m_tokens += src.cache_write_5m_tokens
    dst.cache_write_1h_tokens += src.cache_write_1h_tokens
    dst.cache_read_tokens += src.cache_read_tokens
    dst.messages += src.messages
    dst.cost_usd_accum += src.cost_usd_accum
    dst.cache_read_cost_usd_accum += src.cache_read_cost_usd_accum


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
    # result size per (role, family, tool, subcommand) -- minor #2 (report
    # 9d): result size broken out per subcommand, not just per tool.
    direct_cli_result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))
    skill_invocations: Counter = field(default_factory=Counter)       # (role, family, tool) -> count
    mcp_calls: Counter = field(default_factory=Counter)                # (role, family, tool_name) -> count

    grep_pattern_classes: Counter = field(default_factory=Counter)     # (role, family, class) -> count
    grep_pattern_result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))  # (role,family,class)->SizeStats
    grep_to_read_chains: Counter = field(default_factory=Counter)      # (role, family) -> chain count

    spec_path_classes: Counter = field(default_factory=Counter)        # (role, family, class) -> count
    spec_path_result_sizes: dict = field(default_factory=lambda: defaultdict(SizeStats))  # (role,family,class)->SizeStats
    spec_hunt_via_git_or_grep: Counter = field(default_factory=Counter)  # (role, family) -> count

    def record_direct_cli(self, role: str, family: str, tool: str, subcommand: str) -> None:
        self.direct_cli_calls[(role, family, tool, subcommand)] += 1

    def record_direct_cli_result(self, role: str, family: str, tool_subcommand: tuple, chars: int) -> None:
        self.direct_cli_result_sizes[(role, family) + tuple(tool_subcommand)].add(chars)

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
    """Only used transiently to test a short regex (a permission-denial
    hint, or minor #8's auto-background signal) against a tool_result's
    own content; the matched text itself is never stored or written out."""
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
# minor #8: the ORIGINAL pattern matched the assistant's own free-text
# prose about backgrounding (e.g. narrating what it just did), not a real
# signal from the harness -- confirmed wrong against real transcripts,
# where the harness's own auto-background notice is a specific, literal
# tool_result string: "Command did not complete within its <N>s timeout
# and was moved to the background (ID: <id>)." Match THAT, and match it
# against the Bash tool_result's own content, never the prior assistant
# turn's prose (which is what `gap_prev_text` holds).
AUTOBG_TIMEOUT_RE = re.compile(
    r"did not complete within its \d+s timeout and was moved to the background",
    re.IGNORECASE,
)
# a real `<task-notification>` wrapper (background command / subagent
# completion), as delivered by the harness -- confirmed against real
# transcripts, e.g. '<task-notification>\n<task-id>...'
TASK_NOTIFICATION_RE = re.compile(r"<task-notification>", re.IGNORECASE)


def classify_stall_gap(rec: dict, msg: dict, pending_tool_ids: set, prev_text: str = "") -> str:
    """M1: classify what closed a >5-minute gap between two records (of
    ANY type -- assistant or user), instead of only ever looking at
    consecutive assistant-to-assistant turns:

    - "tool_running": the gap-closing record is a tool_result for a
      tool_use that was already open before the gap started (a long-running
      Bash call, etc.) -- or any tool_result, as a weaker signal.
    - "waiting_notification": the gap-closing record carries a real
      `<task-notification>` wrapper (background command / subagent done),
      or the turn BEFORE the gap said it was waiting for one.
    - "idle_until_resume": anything else -- most commonly a fresh user turn
      (e.g. via SendMessage) arriving after the agent went idle at the end
      of a turn.
    """
    content = msg.get("content")
    text = extract_text(content)
    if TASK_NOTIFICATION_RE.search(text or "") or WAITING_RE.search(prev_text or ""):
        return "waiting_notification"
    if rec.get("type") == "user":
        blocks = content if isinstance(content, list) else []
        tool_result_ids = {
            b.get("tool_use_id") for b in blocks
            if isinstance(b, dict) and b.get("type") == "tool_result"
        }
        if tool_result_ids:
            return "tool_running"
    return "idle_until_resume"


def advance_stall_gap(rec: dict, msg: dict, ts: Optional[datetime],
                       since: datetime, until: datetime,
                       gap_prev_ts: Optional[datetime], gap_prev_text: str,
                       pending_tool_ids: set, role: str, session_id: str,
                       loop_events: Optional[list] = None,
                       counter: Optional[Counter] = None,
                       minutes_counter: Optional[Counter] = None) -> tuple:
    """NB1: the gap timer must be advanced by EVERY record with a
    timestamp, of any type and on any day -- otherwise a genuine multi-day
    gap (the agent's session simply idle overnight) looks, to the next
    in-window record, like the FULL elapsed time since the last in-window
    record was seen, which is wrong in the other direction too. But a gap
    is only ever COUNTED as a stall when the record that closes it falls
    inside the window -- a stall belongs to the day it was noticed on, not
    the day the prior activity happened to be.

    `minutes_counter`, when given, accumulates the gap's own duration (not
    just a +1 count) under its kind -- counts alone can't be used to judge
    wall-clock impact (a SUMMARY headline needs stall MINUTES, not just how
    many).

    Major 1 (round 3): whether a gap counts as a stall at all is still
    decided from its FULL duration (a genuine multi-day gap is still a
    stall the day it's noticed) -- but the MINUTES booked for it are
    clipped to `max(gap_prev_ts, since)`, so a gap that opened on an
    earlier day only books the portion that actually fell inside THIS
    window. Otherwise a session idle for several days would book more
    than a day of stall minutes onto the single day that happened to
    close it.

    Returns the (possibly updated) (gap_prev_ts, gap_prev_text) pair.
    """
    if ts is None:
        return gap_prev_ts, gap_prev_text
    if gap_prev_ts and in_window(ts, since, until):
        gap_seconds = (ts - gap_prev_ts).total_seconds()
        if gap_seconds > STALL_THRESHOLD_SECONDS:
            kind = classify_stall_gap(rec, msg, pending_tool_ids, gap_prev_text)
            clipped_start = max(gap_prev_ts, since)
            clipped_seconds = (ts - clipped_start).total_seconds()
            if loop_events is not None:
                loop_events.append(("stall", role, session_id, kind, clipped_seconds))
            if counter is not None:
                counter[kind] += 1
            if minutes_counter is not None:
                minutes_counter[kind] += clipped_seconds / 60.0
    return ts, extract_text(msg.get("content"))


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


def _merge_usage_max(dst: Optional[dict], src: dict) -> dict:
    """Per-field MAX merge of two `usage` objects seen for the SAME
    requestId across several streamed JSONL lines (B2): real transcripts
    show the same requestId's usage growing line to line as the response
    streams in (e.g. output_tokens 11 then 114), so the correct total is
    the max per field, not the first line (undercounts) and not a naive
    sum (double-counts)."""
    if dst is None:
        return dict(src)
    out = dict(dst)
    for k in ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"):
        out[k] = max(int(dst.get(k) or 0), int(src.get(k) or 0))
    dst_cc = dst.get("cache_creation") if isinstance(dst.get("cache_creation"), dict) else {}
    src_cc = src.get("cache_creation") if isinstance(src.get("cache_creation"), dict) else {}
    if dst_cc or src_cc:
        out["cache_creation"] = {
            "ephemeral_5m_input_tokens": max(int(dst_cc.get("ephemeral_5m_input_tokens") or 0),
                                              int(src_cc.get("ephemeral_5m_input_tokens") or 0)),
            "ephemeral_1h_input_tokens": max(int(dst_cc.get("ephemeral_1h_input_tokens") or 0),
                                              int(src_cc.get("ephemeral_1h_input_tokens") or 0)),
        }
    if src.get("iterations"):
        out["iterations"] = src["iterations"]
    elif "iterations" not in out and dst.get("iterations"):
        out["iterations"] = dst["iterations"]
    return out


def process_transcript(path: str, since: datetime, until: datetime, role: str,
                        session_id: str, tool_agg: ToolCallAgg,
                        bash_verb_totals: Counter, bash_subverb_totals: Counter,
                        long_bash: list, autobg_total: list, autobg_timeout_total: list,
                        dispatch_targets: Counter, resume_targets: Counter,
                        cpu_heavy_counter: Counter, loop_events: list,
                        pipe_counter: Counter, seq_miner: SequenceMiner,
                        adoption_agg: Optional["AdoptionAgg"] = None,
                        default_model_family: str = "unknown",
                        pipe_filter_totals: Optional[Counter] = None) -> Optional[SessionStats]:
    """Shared pass over one transcript file (main session or subagent),
    tagged with `role` ("main" or "subagent") for the tool-call breakdown."""
    st = SessionStats(session_id=session_id)
    if pipe_filter_totals is None:
        pipe_filter_totals = Counter()
    pending_tool_use: dict = {}  # tool_use_id -> (timestamp, name, input, adoption_meta)
    pending_tool_ids: set = set()  # tool_use ids not yet closed by a tool_result (M1)
    touched = False
    session_bash_order: list = []
    gap_prev_ts: Optional[datetime] = None  # last activity of ANY record type, for M1 gap classification
    gap_prev_text = ""
    cur_family = default_model_family
    symbol_lookup_ttl = 0  # tool_use calls remaining in which a Read "consumes" a pending symbol-like grep
    # One API response is often split across several JSONL lines (one per
    # content block), each repeating (and, per B2, sometimes GROWING) the
    # same requestId's usage. Buffer per requestId and flush once at EOF,
    # taking the per-field max rather than the first-seen (B2) or a naive
    # sum of every line (B1's double-count via `iterations`, resolved
    # inside UsageBucket.add()).
    request_buffer: dict = {}  # requestId (or synthetic key) -> {"usage", "ts", "model"}
    noid_counter = 0
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
            # NB1: advance the gap timer from every record, in or out of
            # window, before deciding whether THIS record's own data is
            # in-window and worth accumulating.
            gap_prev_ts, gap_prev_text = advance_stall_gap(
                rec, msg, ts, since, until, gap_prev_ts, gap_prev_text,
                pending_tool_ids, role, session_id, loop_events)
            if ts is None or not in_window(ts, since, until):
                # minor #8 (round 3): still register this OUT-of-window
                # record's own tool_use ids -- otherwise the first
                # in-window gap that a later tool_result closes has
                # nothing pending to match against, and is misclassified
                # idle_until_resume instead of tool_running. This is
                # bookkeeping only (no window-gated metric reads it);
                # everything else about this record stays skipped.
                for block in msg.get("content") or []:
                    if isinstance(block, dict) and block.get("type") == "tool_use":
                        tid = block.get("id")
                        if tid:
                            pending_tool_use[tid] = (ts, block.get("name") or "?", block.get("input") or {}, None)
                            pending_tool_ids.add(tid)
                continue
            touched = True
            model = msg.get("model")
            if model:
                cur_family = model_family(model)
            if msg.get("isCompactSummary"):
                compactions_via_summary_flag += 1
            usage = msg.get("usage") or {}
            request_id = rec.get("requestId") or msg.get("id")
            if usage:
                key = request_id if request_id is not None else f"__noid__{noid_counter}"
                if request_id is None:
                    noid_counter += 1
                prev_entry = request_buffer.get(key)
                merged_usage = _merge_usage_max(prev_entry["usage"] if prev_entry else None, usage)
                resolved_model = model or (prev_entry["model"] if prev_entry else None)
                request_buffer[key] = {"usage": merged_usage, "ts": ts, "model": resolved_model}

            for block in msg.get("content") or []:
                if not isinstance(block, dict):
                    continue
                if block.get("type") == "tool_use":
                    # minor #6: the grep->Read window must count 3 FULL
                    # tool calls after the grep, not 2 -- decrementing the
                    # TTL at the TOP of this call (before this call's own
                    # branch gets to check or set it) burns one call of
                    # budget on the very call that's supposed to still be
                    # inside the window. Only decrement once, at the END
                    # of this call's processing, and only if this call
                    # neither set a fresh TTL nor consumed it.
                    ttl_touched_this_call = False
                    name = block.get("name") or "?"
                    tinput = block.get("input") or {}
                    st.tool_use_counts[name] += 1
                    tool_agg.record_call(role, cur_family, name, session_id, tinput)
                    adoption_meta = None  # (kind, class_label) to resolve against the tool_result size

                    if name == "Bash":
                        cmd = strip_heredocs(tinput.get("command") or "")
                        segments = list(iter_command_segments(cmd))
                        # M2: classify EVERY top-level command-position
                        # segment of a `a && b && c` chain, not just the
                        # first -- `cd x && go test ./...` must not count
                        # as `cmd:cd`. NM2: a `|`-downstream segment
                        # (e.g. `head`/`grep` filtering a pipeline) is not
                        # a new command position -- it never gets its own
                        # verb-table entry, only pipe_filter_totals, and it
                        # is never fed to the sequence miner.
                        first_call_verb = None
                        for verb, subverb, seg_str, is_pipe_stage, seg_toks in segments:
                            if is_pipe_stage:
                                coarse = _classify_bash_verb_segment(verb, subverb, seg_str, seg_toks)
                                pipe_filter_totals[coarse] += 1
                                continue
                            coarse = _classify_bash_verb_segment(verb, subverb, seg_str, seg_toks)
                            st.bash_verbs[coarse] += 1
                            bash_verb_totals[coarse] += 1
                            bash_subverb_totals[subverb] += 1
                            if first_call_verb is None:
                                first_call_verb = coarse
                        # NM2: the sequence miner works over Bash CALLS, one
                        # entry per tool_use -- its first classified verb --
                        # not per segment.
                        if first_call_verb is not None:
                            session_bash_order.append(first_call_verb)

                        # CPU-heavy / wb-run-wrapping at CLAUSE granularity
                        # (minor #1): `wb run` wraps only the clause it
                        # prefixes, and a quoted mention never matches
                        # (is_cpu_heavy is token-aware).
                        for effective_text, wrapped in cpu_heavy_clauses(cmd):
                            if is_cpu_heavy(effective_text):
                                st.cpu_heavy_total += 1
                                cpu_heavy_counter["total"] += 1
                                cpu_heavy_counter[f"total_{role}"] += 1
                                if not wrapped:
                                    st.cpu_heavy_unwrapped += 1
                                    cpu_heavy_counter["unwrapped"] += 1
                                    cpu_heavy_counter[f"unwrapped_{role}"] += 1

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
                            # M2: run the specscore/codegrapher/grep/spec-path
                            # classifiers over every segment too; the LAST
                            # segment that produces a classification is kept
                            # as this tool_use's adoption_meta so the single
                            # tool_result size is attributed once, not
                            # multiply, across segments.
                            for verb, subverb, seg_str, _is_pipe_stage, seg_toks in segments:
                                if verb in CLI_KNOWLEDGE_TOOLS:
                                    # minor #1: read the subcommand from the
                                    # real TOKEN list, never by re-splitting
                                    # seg_str (which has already lost the
                                    # quoting of any multi-word argument),
                                    # and only surface it if it's a known
                                    # subcommand -- else "(other)".
                                    allowlist = (SPECSCORE_SUBCOMMANDS if verb == "specscore"
                                                 else CODEGRAPHER_SUBCOMMANDS)
                                    raw_sub = seg_toks[1] if len(seg_toks) > 1 else ""
                                    sub = raw_sub if raw_sub in allowlist else "(other)"
                                    adoption_agg.record_direct_cli(role, cur_family, verb, sub)
                                    adoption_meta = ("direct_cli", (verb, sub))
                                    symbol_lookup_ttl = 0  # a real CLI query resolves any pending lookup
                                    ttl_touched_this_call = True
                                elif verb in ("rg", "grep") or subverb == "git grep":
                                    pattern = extract_bash_grep_pattern(seg_str)
                                    cls = classify_grep_pattern(pattern or "")
                                    adoption_agg.record_grep_pattern(role, cur_family, cls)
                                    adoption_meta = ("grep", cls)
                                    if cls == "symbol-like":
                                        symbol_lookup_ttl = 3
                                        ttl_touched_this_call = True
                                    if "spec/" in seg_str:
                                        adoption_agg.spec_hunt_via_git_or_grep[(role, cur_family)] += 1
                                elif subverb == "git log" and "spec/" in seg_str:
                                    adoption_agg.spec_hunt_via_git_or_grep[(role, cur_family)] += 1
                                else:
                                    read_path = extract_bash_read_path(seg_str)
                                    if read_path:
                                        spec_cls = classify_spec_path(read_path)
                                        if spec_cls:
                                            adoption_agg.record_spec_path(role, cur_family, spec_cls)
                                            adoption_meta = ("spec_path", spec_cls)
                                        elif looks_like_source_file(read_path) and symbol_lookup_ttl > 0:
                                            adoption_agg.grep_to_read_chains[(role, cur_family)] += 1
                                            symbol_lookup_ttl = 0
                                            ttl_touched_this_call = True

                    if name == "Grep" and adoption_agg is not None:
                        pattern = tinput.get("pattern")
                        if pattern is not None:
                            cls = classify_grep_pattern(pattern)
                            adoption_agg.record_grep_pattern(role, cur_family, cls)
                            adoption_meta = ("grep", cls)
                            if cls == "symbol-like":
                                symbol_lookup_ttl = 3
                                ttl_touched_this_call = True
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
                            symbol_lookup_ttl = 0
                            ttl_touched_this_call = True

                    if name == "Skill" and adoption_agg is not None:
                        skill_name = str(tinput.get("skill") or "")
                        if skill_name.startswith("codegrapher"):
                            adoption_agg.record_skill(role, cur_family, "codegrapher")
                        elif skill_name.startswith("specscore"):
                            adoption_agg.record_skill(role, cur_family, "specscore")

                    if name.startswith("mcp__codegrapher") and adoption_agg is not None:
                        adoption_agg.record_mcp(role, cur_family, name)

                    # minor #6: only spend one call of the grep->Read
                    # window's budget per call, and never on the call that
                    # just set or consumed it (see the comment above).
                    if not ttl_touched_this_call and symbol_lookup_ttl > 0:
                        symbol_lookup_ttl -= 1

                    tid = block.get("id")
                    pending_tool_use[tid] = (ts, name, tinput, adoption_meta)
                    pending_tool_ids.add(tid)

                    if name == "Agent":
                        st.dispatch_count += 1
                        dispatch_targets[tinput.get("subagent_type") or "unspecified"] += 1
                    if name == "SendMessage":
                        if tinput.get("to"):
                            st.resume_count += 1
                            resume_targets[resume_target_label(tinput.get("to"))] += 1

        else:  # user message: tool_result timing, error/denial, size
            # NB1: a user record (e.g. a tool_result, or a fresh resume via
            # SendMessage) can close a gap that started on an earlier day;
            # only count it as a stall if THIS closing record is in-window.
            gap_prev_ts, gap_prev_text = advance_stall_gap(
                rec, msg, ts, since, until, gap_prev_ts, gap_prev_text,
                pending_tool_ids, role, session_id, loop_events)
            # NB1: a session whose ONLY in-window activity is a user record
            # (e.g. it closes a stall gap that opened the day before, with
            # no assistant record of its own falling inside the window)
            # must still count as "touched" -- otherwise the whole
            # transcript is dropped as untouched and the very stall NB1
            # exists to catch is silently lost downstream.
            if ts is not None and in_window(ts, since, until):
                touched = True
            content = msg.get("content")
            if isinstance(content, list):
                for block in content:
                    if not isinstance(block, dict) or block.get("type") != "tool_result":
                        continue
                    tid = block.get("tool_use_id")
                    entry = pending_tool_use.get(tid)
                    if not entry:
                        continue
                    del pending_tool_use[tid]
                    pending_tool_ids.discard(tid)
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
                    if name == "Bash":
                        # minor #8: check the harness's own literal signal
                        # against THIS tool_result's own content, not a
                        # prior assistant turn's prose (see AUTOBG_TIMEOUT_RE).
                        result_text = result_content_text_for_denial_check(block.get("content"))
                        if AUTOBG_TIMEOUT_RE.search(result_text):
                            st.autobg_timeout_count += 1
                            autobg_timeout_total.append(1)
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

    # Flush the per-requestId usage buffer once, priced by each request's
    # own model (B1/B2/B3/M3); dict insertion order approximates the
    # chronological order the requests first appeared in.
    for entry in request_buffer.values():
        entry_ts, entry_usage, entry_model = entry["ts"], entry["usage"], entry["model"]
        st.day_usage[day_key(entry_ts)].add(entry_usage, model=entry_model)
        fam = model_family(entry_model) if entry_model else cur_family
        st.model_usage[fam].add(entry_usage, model=entry_model)
        ctx = (int(entry_usage.get("input_tokens") or 0)
               + int(entry_usage.get("cache_read_input_tokens") or 0)
               + int(entry_usage.get("cache_creation_input_tokens") or 0))
        st.ctx_series.append(ctx)
        if st.first_turn_ctx is None:
            st.first_turn_ctx = ctx
        st.last_turn_ctx = ctx

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
    day_usage: dict = field(default_factory=lambda: defaultdict(UsageBucket))  # minor #3
    tool_uses: int = 0
    stalls_by_kind: Counter = field(default_factory=Counter)  # M1: tool_running/idle_until_resume/waiting_notification
    stall_minutes_by_kind: Counter = field(default_factory=Counter)  # SUMMARY headline: minutes, not just counts
    dispatch_tool_use_id: Optional[str] = None
    agent_type: Optional[str] = None
    description_present: bool = False

    @property
    def stalls(self) -> int:
        return sum(self.stalls_by_kind.values())

    @property
    def stall_ending_waiting(self) -> int:
        return self.stalls_by_kind.get("waiting_notification", 0)


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
    touched = False
    gap_prev_ts: Optional[datetime] = None  # M1: gap since the last record of ANY type
    gap_prev_text = ""
    pending_tool_ids: set = set()
    pending_tool_use: dict = {}  # tool_use_id -> timestamp, so a tool_result can close it
    request_buffer: dict = {}  # requestId (or synthetic key) -> {"usage", "ts", "model"} -- B1/B2
    noid_counter = 0

    for rec in iter_jsonl(path):
        ts = parse_ts(rec.get("timestamp"))
        rtype = rec.get("type")
        if rtype not in ("assistant", "user"):
            continue
        msg = rec.get("message") or {}

        if rtype == "assistant":
            gap_prev_ts, gap_prev_text = advance_stall_gap(
                rec, msg, ts, since, until, gap_prev_ts, gap_prev_text,
                pending_tool_ids, "subagent", agent_id, counter=a.stalls_by_kind, minutes_counter=a.stall_minutes_by_kind)
            if ts is None or not in_window(ts, since, until):
                continue
            touched = True
            model = msg.get("model")
            if model:
                models[model] += 1
            usage = msg.get("usage") or {}
            request_id = rec.get("requestId") or msg.get("id")
            if usage:
                key = request_id if request_id is not None else f"__noid__{noid_counter}"
                if request_id is None:
                    noid_counter += 1
                prev_entry = request_buffer.get(key)
                merged_usage = _merge_usage_max(prev_entry["usage"] if prev_entry else None, usage)
                resolved_model = model or (prev_entry["model"] if prev_entry else None)
                request_buffer[key] = {"usage": merged_usage, "ts": ts, "model": resolved_model}
            if a.first_ts is None or ts < a.first_ts:
                a.first_ts = ts
            if a.last_ts is None or ts > a.last_ts:
                a.last_ts = ts
            for block in msg.get("content") or []:
                if isinstance(block, dict) and block.get("type") == "tool_use":
                    a.tool_uses += 1
                    tid = block.get("id")
                    pending_tool_use[tid] = ts
                    pending_tool_ids.add(tid)
        else:  # user: closes pending tool_use ids, and may itself be a
               # task-notification or a fresh resume that ends a stall gap
            gap_prev_ts, gap_prev_text = advance_stall_gap(
                rec, msg, ts, since, until, gap_prev_ts, gap_prev_text,
                pending_tool_ids, "subagent", agent_id, counter=a.stalls_by_kind, minutes_counter=a.stall_minutes_by_kind)
            # NB1: a subagent transcript whose ONLY in-window activity is
            # this closing user record (its assistant turn fell the day
            # before) must still count as "touched" -- else the whole
            # transcript, and the in-window stall it just closed, is
            # dropped.
            if ts is not None and in_window(ts, since, until):
                touched = True
            content = msg.get("content")
            if isinstance(content, list):
                for block in content:
                    if isinstance(block, dict) and block.get("type") == "tool_result":
                        tid = block.get("tool_use_id")
                        pending_tool_use.pop(tid, None)
                        pending_tool_ids.discard(tid)

    if not touched:
        return None

    # Flush the per-requestId usage buffer once, priced by each request's
    # own model and booked on the day it actually happened (minor #3), not
    # unconditionally under the agent's first day.
    for entry in request_buffer.values():
        entry_ts, entry_usage, entry_model = entry["ts"], entry["usage"], entry["model"]
        a.usage.add(entry_usage, model=entry_model)
        a.day_usage[day_key(entry_ts)].add(entry_usage, model=entry_model)

    # meta.json's declared model is authoritative when present AND not the
    # generic "inherit" placeholder (M3: "inherit" or missing means the
    # transcript's own majority-vote observed model is the real label, so
    # the agent groups/prices correctly instead of falling into "other").
    declared = meta.get("model")
    if declared and str(declared).strip().lower() not in ("inherit", ""):
        a.model = str(declared)
    elif models:
        a.model = models.most_common(1)[0][0]
    a.agent_type = meta.get("agentType")
    a.description_present = bool(meta.get("description"))
    return a


def _looks_like_transcript(path: str) -> bool:
    """Minor #6: the `/tmp/.../tasks/*.output` glob also matches plain
    background-Bash stdout files (not JSONL transcripts at all -- about
    1,160 of them on real data). Include a file only if its first non-empty
    line parses as JSON and has a `sessionId` or `type` key."""
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                except json.JSONDecodeError:
                    return False
                return isinstance(obj, dict) and ("sessionId" in obj or "type" in obj)
    except OSError:
        return False
    return False  # an empty file is not a transcript


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
        if not _looks_like_transcript(p):
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
                         raw_dir: Optional[Path] = None, home: Optional[Path] = None,
                         offline: bool = False, repos_out: Optional[set] = None) -> dict:
    """Scan one or more WB state directories (a fleet may have used
    `~/.wb` and later `~/projects/.wb`, or both at once) and merge them,
    de-duplicating events seen under more than one root by (type, claim_id,
    run_id).

    `repos_out`, when given, is filled with every distinct `repository`
    seen on a claim or seal event that falls inside the window (Major 2,
    round 3: the fleet-wide "$ per merged PR" denominator's repo set) --
    kept OUT of the returned dict itself, since metrics.json otherwise
    would end up listing every repository the fleet has touched, private
    or not, which nothing before this needed to expose."""
    existing_dirs = [d for d in wb_state_dirs if d.exists()]
    if not existing_dirs:
        return {"unavailable": f"no WB state dir found among {scrub_home_path([str(d) for d in wb_state_dirs])}"}

    out = {
        "state_dirs_scanned": scrub_home_path([str(d) for d in existing_dirs]),
        "claimed": 0, "sealed": 0,
        "disposition": Counter(),
        "landing_durations_s": [],
    }

    # minor #7: collect ALL claims across BOTH state dirs in one pass first,
    # then match seals against the complete set -- a worktree's claim and
    # its seal can land in different homes (~/.wb vs ~/projects/.wb) if the
    # fleet switched roots mid-flight, and processing directory-by-directory
    # could miss a cross-home match depending on scan order.
    all_events: list = []  # (dedupe_key, type, record)
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
            all_events.append((t, d))

    claims: dict = {}
    for t, d in all_events:
        if t == "worktree.claimed":
            out["claimed"] += 1
            ts = parse_ts(d.get("at"))
            if ts:
                claims[d.get("claim_id")] = ts
                if in_window(ts, since, until) and d.get("repository") and repos_out is not None:
                    repos_out.add(d["repository"])

    for t, d in all_events:
        if t != "worktree.sealed":
            continue
        ts = parse_ts(d.get("at"))
        # minor #7 (round 3): a seal record with NO timestamp must be
        # excluded, not silently treated as in-window -- `ts and not
        # in_window(...)` short-circuits to False (i.e. "don't skip") when
        # `ts` is None, which is backwards.
        if not ts or not in_window(ts, since, until):
            continue
        out["sealed"] += 1
        out["disposition"][d.get("disposition") or "unknown"] += 1
        if d.get("repository") and repos_out is not None:
            repos_out.add(d["repository"])
        claimed_at = claims.get(d.get("claim_id"))
        if claimed_at and ts:
            out["landing_durations_s"].append((ts - claimed_at).total_seconds())

    out["repos_with_in_window_activity_count"] = len(repos_out) if repos_out is not None else None

    out["disposition"] = dict(out["disposition"])
    if out["landing_durations_s"]:
        out["landing_duration_median_s"] = statistics.median(out["landing_durations_s"])
        out["landing_duration_p90_s"] = (
            sorted(out["landing_durations_s"])[int(0.9 * (len(out["landing_durations_s"]) - 1))]
        )
    del out["landing_durations_s"]

    # minor #10: with --offline, replay the wait-snapshot files this pass
    # already cached under raw/waits/ on an earlier live run, rather than
    # re-scanning the live (and likely since-changed) waits/ directories.
    wait_kinds = Counter()
    any_waits_dir = False
    if offline:
        if raw_dir:
            cached_waits_dir = raw_dir / "waits"
            wait_files = sorted(glob.glob(str(cached_waits_dir / "*.json")))
            any_waits_dir = bool(wait_files)
            for fp in wait_files:
                try:
                    d = json.loads(Path(fp).read_text(encoding="utf-8"))
                except Exception:
                    continue
                wait_kinds[d.get("kind") or "unknown"] += 1
    else:
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
                if raw_dir:
                    # NM4: a wait-snapshot file carries `pid`,
                    # `wb_session_id`, `targets` and `resume_args` -- the
                    # last two can name any repo/PR the fleet is watching,
                    # not just this one. Declare and keep only the two
                    # fields this pass actually reads: `kind` and
                    # `started_at` (never the whole file).
                    dest_dir = raw_dir / "waits"
                    dest_dir.mkdir(parents=True, exist_ok=True)
                    try:
                        dest = dest_dir / (hashlib.sha1(fp.encode()).hexdigest()[:16] + ".json")
                        trimmed = {"kind": d.get("kind"), "started_at": d.get("started_at")}
                        dest.write_text(json.dumps(trimmed, sort_keys=True), encoding="utf-8")
                    except Exception:
                        pass
    if any_waits_dir:
        out["open_wait_snapshots_by_kind"] = dict(wait_kinds)
        # minor #5 (round 3): explicitly a live SNAPSHOT, not window-
        # filtered data -- a wait started before or after the window is
        # still counted here if its file was on disk at extraction time,
        # because wb keeps no durable wait-history log to filter against.
        out["open_wait_note"] = (
            "SNAPSHOT, not filtered to the window: counts wait files present on disk "
            "at extraction time, regardless of when each wait actually started (wb "
            "keeps no durable wait-history log to filter against)"
            + (" (replayed from raw/ cache: --offline)" if offline else "")
        )
    else:
        out["open_wait_snapshots_by_kind"] = "unavailable: no waits dir found under any scanned state dir"

    out["admission_queue"] = "unavailable: wb run admission/queue history is not persisted to disk"
    out["refusal_codes"] = "unavailable: worklog events carry disposition, not a structured refusal-code taxonomy"

    # minor #4: hook events live under the SCANNED --home, not necessarily
    # the process's own $HOME (they can differ, e.g. under --home in tests
    # or when scanning another user's state).
    effective_home = home if home is not None else Path.home()
    out["hook_events"] = process_hook_events(effective_home / ".local" / "state" / "wb" / "hook-events.jsonl",
                                              since, until)
    out["surviving_run_events"] = process_surviving_run_events(home_projects_root(existing_dirs, effective_home),
                                                                 since, until, raw_dir, offline=offline)
    return out


def home_projects_root(existing_wb_state_dirs: list, home: Optional[Path] = None) -> Path:
    """Best-effort projects root to search for surviving worktree run-event
    files: the parent of whichever scanned .wb dir looks like
    <home>/projects/.wb. Minor #5: when only <home>/.wb exists (no
    <home>/projects/.wb), worktrees still live under <home>/projects/
    .worktrees/ -- falling back to .wb's own parent (<home> itself) glob-
    misses every worktree, so the fallback is <home>/projects explicitly."""
    for d in existing_wb_state_dirs:
        if d.name == ".wb" and d.parent.name == "projects":
            return d.parent
    base = home if home is not None else Path.home()
    return base / "projects"


def process_hook_events(path: Path, since: datetime, until: datetime) -> dict:
    if not path.exists():
        return {"unavailable": f"hook-events log not found at {scrub_home_path(str(path))}"}
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


def _sanitize_run_event_fields(rec: dict) -> dict:
    """NM4: a `wb run` receipt carries `repository` (any repo the fleet has
    touched, not just this one), `effort_id` (a task/worktree name),
    `run_id` and `operation_id` (WB session-shaped ids) -- none of which
    this pass reads. Declare and keep only the four fields it actually
    computes from: timestamp (for the window check), state, kind, and
    queue_wait_ms."""
    return {
        "timestamp": rec.get("timestamp"),
        "state": rec.get("state"),
        "kind": rec.get("kind"),
        "queue_wait_ms": rec.get("queue_wait_ms"),
    }


def process_surviving_run_events(projects_root: Path, since: datetime, until: datetime,
                                  raw_dir: Optional[Path], offline: bool = False) -> dict:
    """`wb run` command-mode receipts live at <worktree>/.wb/local/run/events.jsonl
    and are deleted with the worktree, so only whatever worktrees still exist
    at extraction time can be read. This is a lower bound, not a full count.

    Minor #10: with --offline, replay the copies this pass already cached
    under raw/wb_run_events/ on an earlier live run, instead of re-globbing
    (and finding nothing, since worktrees are commonly gone by the next run).

    NM4: the raw/ cache holds only the four declared fields
    (_sanitize_run_event_fields) for IN-WINDOW events -- never the whole
    file, never repository/effort_id/run_id/operation_id, and never an
    out-of-window event. Restricting both the live count and the cache to
    the same in-window set (rather than caching everything and filtering
    on read) is what keeps an --offline replay's numbers identical to the
    live run's: both count exactly what got cached, nothing more."""
    if offline:
        if not raw_dir:
            return {"unavailable": "--offline given but no raw/ cache directory to replay from"}
        files = sorted(glob.glob(str(raw_dir / "wb_run_events" / "*.jsonl")))
        if not files:
            return {"unavailable": f"--offline given but no cached raw/wb_run_events/ files under {scrub_home_path(str(raw_dir))}"}
        # Major 3 (round 3): report the same `len(files)` an online run
        # would -- offline replay finds the same cached files a live run
        # just wrote, so there's no reason for this to differ.
        surviving_files = len(files)
    else:
        # worktree layout is <task>/<host>/<owner>/<repo>/.wb/local/run/events.jsonl
        # (e.g. .worktrees/my-task/github.com/sneat-dev/wb/.wb/...); glob
        # recursively rather than hard-coding the segment count, which has
        # changed before (see the wb layout-migrations log).
        files = sorted(glob.glob(str(projects_root / ".worktrees" / "**" / ".wb" / "local" / "run" / "events.jsonl"),
                                  recursive=True))
        if not files:
            return {"unavailable": f"no surviving <worktree>/.wb/local/run/events.jsonl files under {scrub_home_path(str(projects_root))}/.worktrees"}
        surviving_files = len(files)
    in_window_count = 0
    by_state = Counter()
    by_kind = Counter()
    queue_waits_ms = []
    cached_rows: list = []  # trimmed, in-window rows to persist, keyed by source file below
    per_file_rows: dict = defaultdict(list)
    for fp in files:
        for rec in iter_jsonl(fp):
            ts = parse_ts(rec.get("timestamp"))
            if not in_window(ts, since, until):
                continue
            in_window_count += 1
            by_state[rec.get("state") or "unknown"] += 1
            by_kind[rec.get("kind") or "unknown"] += 1
            qw = rec.get("queue_wait_ms")
            if isinstance(qw, (int, float)):
                queue_waits_ms.append(qw)
            if raw_dir and not offline:
                per_file_rows[fp].append(_sanitize_run_event_fields(rec))
    if raw_dir and not offline:
        # Write a cache file for every surviving file, even ones that
        # contributed zero in-window rows: an empty cache file still tells
        # a later --offline run "this worktree existed and had no in-window
        # events", so its events_in_window/by_state/by_kind stay 0/{}/{}
        # -- the same as this live run -- instead of falling through to
        # "no cached files" and returning an "unavailable" shape that the
        # live run never produces. Writing nothing here (the old behaviour)
        # is what broke --offline reproducibility when a run genuinely saw
        # zero in-window `wb run` events.
        dest_dir = raw_dir / "wb_run_events"
        dest_dir.mkdir(parents=True, exist_ok=True)
        for fp in files:
            rows = per_file_rows.get(fp, [])
            try:
                dest = dest_dir / (hashlib.sha1(fp.encode()).hexdigest()[:16] + ".jsonl")
                dest.write_text("\n".join(json.dumps(r, sort_keys=True) for r in rows) + ("\n" if rows else ""),
                                 encoding="utf-8")
            except Exception:
                pass
    return {
        "surviving_files": surviving_files,
        "events_in_window": in_window_count,
        "by_state_in_window": dict(by_state),
        "by_kind_in_window": dict(by_kind.most_common(20)),
        "queue_wait_ms_median": statistics.median(queue_waits_ms) if queue_waits_ms else None,
        "note": "lower bound only: events for any worktree already cleaned up before this "
                "extraction ran are gone, and the raw/ cache (and this count) only ever "
                "holds events already inside the window"
                + (" (replayed from raw/ cache: --offline)" if offline else ""),
    }


# --------------------------------------------------------------------------
# GitHub pass
# --------------------------------------------------------------------------

def _sanitize_pr_fields(pr: dict) -> dict:
    """M4: `raw/pulls.json` previously stored the FULL PR object, including
    the PR body (may contain anything the author wrote). Cache only the
    fields this pass actually reads: number and the three lifecycle
    timestamps. minor #7: PRs now come from the Search API (`search/issues`
    with a `created:` range), whose issue-shaped items nest `merged_at`
    under `pull_request` rather than at the top level -- read either
    shape so this works whether the item came from the search endpoint
    or (for an older raw/pulls.json cache, or a test fixture) the plain
    pulls endpoint."""
    if not isinstance(pr, dict):
        return pr
    if "items" in pr:  # minor #7: one gh --paginate page of search/issues is an envelope
        return {"items": [_sanitize_pr_fields(it) for it in pr.get("items") or []]}
    merged_at = pr.get("merged_at")
    if merged_at is None and isinstance(pr.get("pull_request"), dict):
        merged_at = pr["pull_request"].get("merged_at")
    return {
        "number": pr.get("number"),
        "created_at": pr.get("created_at"),
        "merged_at": merged_at,
        "closed_at": pr.get("closed_at"),
        "state": pr.get("state"),
    }


def _sanitize_run_fields(run: dict) -> dict:
    """M4: `raw/actions_runs.json` previously stored the full run object,
    including `head_commit.author` (a real name + email). Cache only run
    id, name, event, conclusion, the three timestamps and head_sha."""
    if not isinstance(run, dict):
        return run
    if "workflow_runs" in run:
        return {"workflow_runs": [_sanitize_run_fields(r) for r in run.get("workflow_runs") or []]}
    if "id" in run and "conclusion" in run:
        return {
            "id": run.get("id"),
            "name": run.get("name"),
            "event": run.get("event"),
            "conclusion": run.get("conclusion"),
            "created_at": run.get("created_at"),
            "run_started_at": run.get("run_started_at"),
            "updated_at": run.get("updated_at"),
            "head_sha": run.get("head_sha"),
        }
    return run


def gh_api_paginated(endpoint: str, params: Optional[dict] = None, cache_path: Optional[Path] = None,
                      offline: bool = False,
                      sanitize: Optional[Callable[[dict], dict]] = None) -> list:
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
    if sanitize is not None:
        items = [sanitize(it) for it in items]
    if cache_path:
        cache_path.parent.mkdir(parents=True, exist_ok=True)
        cache_path.write_text(json.dumps(items), encoding="utf-8")
    return items


def process_github(repo: str, since: datetime, until: datetime, raw_dir: Path, offline: bool) -> dict:
    result: dict = {}
    try:
        # minor #7: the Search API's `created:` range qualifier filters
        # server-side to the window, instead of paginating the repo's
        # ENTIRE PR history via the plain `pulls` endpoint (which has no
        # date filter) and discarding most pages client-side.
        q = f"repo:{repo} is:pr created:{since.date()}..{until.date()}"
        raw_pages = gh_api_paginated(
            "search/issues",
            params={"q": q, "per_page": "100"},
            cache_path=raw_dir / "pulls.json", offline=offline, sanitize=_sanitize_pr_fields,
        )
    except Exception as e:
        return {"unavailable": f"gh api pulls (search) failed: {e}"}

    # each gh --paginate page of search/issues is a {"items": [...]} envelope
    prs = []
    for page in raw_pages:
        if isinstance(page, dict) and "items" in page:
            prs.extend(page["items"])
        else:
            prs.append(page)

    # server-side filtering already applied the window; this is a
    # defensive re-check, not a client-side substitute for it.
    window_prs = [pr for pr in prs if in_window(parse_ts(pr.get("created_at")), since, until)]

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

    # Major 2 (round 3): "merged" above counts PRs CREATED in the window
    # that HAPPEN to already be merged by the time this pass runs -- not
    # the same set as PRs actually MERGED in the window (a PR created in
    # the window can merge days later, and a PR created earlier can merge
    # inside the window and would be invisible to the `created:` query).
    # This second, separate query answers "PRs merged in window" honestly,
    # via the Search API's `merged:` qualifier, for the headline's single-
    # repo count (label b).
    try:
        mq = f"repo:{repo} is:pr is:merged merged:{since.date()}..{until.date()}"
        merged_raw_pages = gh_api_paginated(
            "search/issues",
            params={"q": mq, "per_page": "100"},
            cache_path=raw_dir / "pulls_merged.json", offline=offline, sanitize=_sanitize_pr_fields,
        )
        merged_prs = []
        for page in merged_raw_pages:
            if isinstance(page, dict) and "items" in page:
                merged_prs.extend(page["items"])
            else:
                merged_prs.append(page)
        # defensive re-check against the field this query is actually
        # keyed on (merged_at), not created_at
        merged_in_window = len([pr for pr in merged_prs if in_window(parse_ts(pr.get("merged_at")), since, until)])
    except Exception as e:
        merged_in_window = None
        merged_in_window_unavailable = f"gh api pulls (merged search) failed: {e}"
    else:
        merged_in_window_unavailable = None

    result["prs"] = {
        "created_in_window": len(window_prs),
        "merged": merged,
        "closed_unmerged": closed_unmerged,
        "still_open": still_open,
        # Major 2: the honest "merged in window" count, via `merged:`, not
        # `created:` -- this is what the headline's "<repo> PRs merged in
        # window" (label b) reports, never the `created`-keyed `merged` above.
        "merged_in_window": merged_in_window,
        "merged_in_window_unavailable": merged_in_window_unavailable,
        "time_to_merge_hours": {
            "median": round(statistics.median(merge_times_h), 2) if merge_times_h else None,
            "p90": round(sorted(merge_times_h)[int(0.9 * (len(merge_times_h) - 1))], 2) if merge_times_h else None,
            "count": len(merge_times_h),
        },
    }

    try:
        # minor #9: filter server-side to the window via `created=` instead
        # of paginating the whole run history and discarding out-of-window
        # runs client-side (GitHub Actions' list-runs endpoint supports a
        # `created` range filter; the PR list endpoint below does not, so
        # that one still trims client-side after a bounded lookback).
        runs = gh_api_paginated(
            f"repos/{repo}/actions/runs",
            params={"per_page": "100", "created": f"{since.date()}..{until.date()}"},
            cache_path=raw_dir / "actions_runs.json", offline=offline, sanitize=_sanitize_run_fields,
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


_FLEET_PR_SEARCH_BATCH_SIZE = 15  # GitHub's search query has a length limit;
                                   # batch repo: qualifiers to stay well under it


def process_fleet_merged_prs(repos: list, since: datetime, until: datetime,
                              raw_dir: Path, offline: bool) -> dict:
    """Major 2 (round 3): the fleet-wide numerator for "$ per merged PR" is
    every session's cost, across every repo the fleet touched -- so its
    denominator must be PRs merged across that SAME fleet, not one repo's
    PRs. `repos` is every repository with an in-window worktree claim or
    seal (process_wb_worklogs' `repos_out`). Queries the Search API's
    `is:pr is:merged merged:<since>..<until>` with batched `repo:`
    qualifiers (one query per batch, to stay under GitHub's query-length
    limit), and caches each batch's trimmed results under raw/ so an
    `--offline` rerun reproduces the same count."""
    if not repos:
        return {"unavailable": "no repository had an in-window worktree claim or seal"}
    sorted_repos = sorted(repos)
    batches = [sorted_repos[i:i + _FLEET_PR_SEARCH_BATCH_SIZE]
               for i in range(0, len(sorted_repos), _FLEET_PR_SEARCH_BATCH_SIZE)]
    total_merged = 0
    # batches never overlap in repos, so no PR can appear in two batches --
    # dedupe within a batch only (search results can repeat a PR across pages)
    for bi, batch in enumerate(batches):
        q = ("is:pr is:merged merged:" + f"{since.date()}..{until.date()} "
             + " ".join(f"repo:{r}" for r in batch))
        try:
            raw_pages = gh_api_paginated(
                "search/issues",
                params={"q": q, "per_page": "100"},
                cache_path=raw_dir / f"fleet_merged_prs_batch{bi:02d}.json",
                offline=offline, sanitize=_sanitize_pr_fields,
            )
        except Exception as e:
            return {"unavailable": f"gh api fleet merged-PR search failed on batch {bi}: {e}"}
        items = []
        for page in raw_pages:
            if isinstance(page, dict) and "items" in page:
                items.extend(page["items"])
            else:
                items.append(page)
        batch_numbers = set()
        for pr in items:
            if not in_window(parse_ts(pr.get("merged_at")), since, until):
                continue
            num = pr.get("number")
            if num is not None and num in batch_numbers:
                continue
            if num is not None:
                batch_numbers.add(num)
            total_merged += 1
    return {
        "merged_in_window": total_merged,
        "repos_queried": len(sorted_repos),
        "batches": len(batches),
    }


# --------------------------------------------------------------------------
# git log pass
# --------------------------------------------------------------------------

def process_git_log(repo_path: Path, since: datetime, until: datetime) -> dict:
    if not repo_path.exists():
        return {"unavailable": f"repo not found at {scrub_home_path(str(repo_path))}"}
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
        return {"unavailable": f"git log failed: {scrub_home_path(str(e))}"}
    if total.returncode != 0:
        return {"unavailable": f"git log exit {total.returncode}: {scrub_home_path(total.stderr[:200])}"}
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
        return {"unavailable": f"no Codex sessions dir at {scrub_home_path(str(root))}"}
    files = sorted(glob.glob(str(root / "*" / "*" / "*" / "rollout-*.jsonl")))
    if not files:
        return {"unavailable": f"no rollout-*.jsonl files under {scrub_home_path(str(root))}"}

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
                        pipe_counter: Counter, seq_miner: SequenceMiner,
                        pipe_filter_totals: Optional[Counter] = None) -> dict:
    loop_count = sum(1 for e in loop_events if e[0] == "loop")
    loop_durations = [e[3] for e in loop_events if e[0] == "loop_duration"]
    total = cpu_heavy_counter.get("total", 0)
    unwrapped = cpu_heavy_counter.get("unwrapped", 0)
    wb_run_coverage = None
    if total:
        wb_run_coverage = round(1 - (unwrapped / total), 3)

    def role_coverage(role: str) -> Optional[float]:
        t = cpu_heavy_counter.get(f"total_{role}", 0)
        u = cpu_heavy_counter.get(f"unwrapped_{role}", 0)
        return round(1 - (u / t), 3) if t else None

    return {
        "by_verb": dict(bash_verb_totals),
        # NM2: `|`-downstream filters (e.g. `head`/`grep` piped from a
        # real command) are counted separately here, one entry per
        # command POSITION in the pipeline (not per Bash call) -- they are
        # never folded into by_verb, which counts real command positions
        # only, and they never feed the sequence miner (which works over
        # whole Bash CALLS -- see top_multi_call_sequences).
        "pipe_filters": dict(pipe_filter_totals or {}),
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
            # minor #1: main vs subagent split; "wb run" wraps only the
            # clause it prefixes (cpu_heavy_clauses), not siblings in the
            # same `&&` chain.
            "by_role": {
                "main": {
                    "total": cpu_heavy_counter.get("total_main", 0),
                    "not_wrapped_in_wb_run": cpu_heavy_counter.get("unwrapped_main", 0),
                    "wb_run_coverage": role_coverage("main"),
                },
                "subagent": {
                    "total": cpu_heavy_counter.get("total_subagent", 0),
                    "not_wrapped_in_wb_run": cpu_heavy_counter.get("unwrapped_subagent", 0),
                    "wb_run_coverage": role_coverage("subagent"),
                },
            },
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


def build_stalls_section(loop_events: list) -> dict:
    """M1: stalls classified by kind (tool_running / idle_until_resume /
    waiting_notification), reported separately for the main loop AND for
    subagents -- both were previously collapsed into a single noisy
    >5-minute-gap count, and main-loop stalls were dropped entirely."""
    by_role_kind: Counter = Counter()
    minutes_by_role_kind: Counter = Counter()
    for e in loop_events:
        if e[0] != "stall":
            continue
        _, role, _session_id, kind, gap_seconds = e
        by_role_kind[(role, kind)] += 1
        minutes_by_role_kind[(role, kind)] += gap_seconds / 60.0
    out = {}
    for role in ("main", "subagent"):
        kinds = {k: by_role_kind.get((role, k), 0)
                 for k in ("tool_running", "idle_until_resume", "waiting_notification")}
        minutes = {k: round(minutes_by_role_kind.get((role, k), 0.0), 1)
                   for k in ("tool_running", "idle_until_resume", "waiting_notification")}
        out[role] = {"total": sum(kinds.values()), "by_kind": kinds, "minutes_by_kind": minutes}
    return out


def build_headline_section(since: datetime, until: datetime, repo: str,
                            tokens_main_by_model: dict, tokens_subagent_by_model: dict,
                            total_cost: float, stalls_section: dict,
                            gh_stats: dict, wb_stats: dict, fleet_pr_stats: dict) -> dict:
    """A reviewer coming back a week later needs the total before the
    12-item unavailable list buries it. Computed once here and rendered
    as the FIRST section of SUMMARY.md (and the first key other than
    window/generated_at in metrics.json), with `unavailable` moved last.

    Major 2 (round 3): "$ per merged PR" now names both halves of the
    division explicitly, because the two are NOT the same repo scope:
    - (a) `usd_per_fleet_merged_pr`: every session's cost on this machine,
      across every repository (not just `repo`), divided by every PR
      MERGED in the window (via `merged:`) across every repository that
      had an in-window worktree claim or seal (fleet_pr_stats). This is
      the fleet-wide "how much does a landed PR cost" figure.
    - (b) `repo_merged_prs_in_window`: a plain count -- PRs merged in the
      window for `repo` alone (gh_stats' `merged_in_window`, itself keyed
      on `merged:`, never `created:`)."""
    days = max(1, (until.date() - since.date()).days + 1)
    main_usd = sum(d["estimated_usd"] for d in tokens_main_by_model.values())
    subagent_usd = sum(d["estimated_usd"] for d in tokens_subagent_by_model.values())
    cache_read_usd = (sum(d.get("estimated_cache_read_usd", 0.0) for d in tokens_main_by_model.values())
                       + sum(d.get("estimated_cache_read_usd", 0.0) for d in tokens_subagent_by_model.values()))
    output_tokens = (sum(d["output_tokens"] for d in tokens_main_by_model.values())
                      + sum(d["output_tokens"] for d in tokens_subagent_by_model.values()))

    fleet_merged_prs = None
    if isinstance(fleet_pr_stats, dict) and "unavailable" not in fleet_pr_stats:
        fleet_merged_prs = fleet_pr_stats.get("merged_in_window")
    repo_merged_prs = None
    if isinstance(gh_stats, dict) and "unavailable" not in gh_stats:
        repo_merged_prs = gh_stats.get("prs", {}).get("merged_in_window")
    sealed_worktrees = None
    if isinstance(wb_stats, dict) and "unavailable" not in wb_stats:
        sealed_worktrees = wb_stats.get("sealed")

    def _usd_per(n):
        return round(total_cost / n, 2) if n else None

    return {
        "total_usd": round(total_cost, 2),
        "usd_per_day": round(total_cost / days, 2),
        "usd_main": round(main_usd, 2),
        "usd_subagent": round(subagent_usd, 2),
        "cache_read_share_of_usd": round(cache_read_usd / total_cost, 3) if total_cost else None,
        "output_tokens_total": output_tokens,
        "usd_per_fleet_merged_pr": _usd_per(fleet_merged_prs),
        "fleet_merged_prs_in_window": fleet_merged_prs,
        "fleet_repos_queried": fleet_pr_stats.get("repos_queried") if isinstance(fleet_pr_stats, dict) else None,
        f"{repo}_merged_prs_in_window": repo_merged_prs,
        "usd_per_sealed_worktree": _usd_per(sealed_worktrees),
        "sealed_worktrees_in_window": sealed_worktrees,
        "stall_minutes_by_kind": {
            "main": stalls_section["main"]["minutes_by_kind"],
            "subagent": stalls_section["subagent"]["minutes_by_kind"],
        },
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
    for (role, family, tool, subcommand), stats in adoption_agg.direct_cli_result_sizes.items():
        direct_cli_sizes.setdefault(role, {}).setdefault(family, {}).setdefault(tool, {}).setdefault(
            "result_size_by_subcommand", {})[subcommand] = stats.summary()

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
        return {"unavailable": f"repo not found at {scrub_home_path(str(repo_path))}"}
    try:
        proc = subprocess.run(["codegrapher", "status"], cwd=str(repo_path),
                               capture_output=True, text=True, timeout=30)
    except FileNotFoundError:
        return {"unavailable": "codegrapher CLI not found on PATH"}
    except Exception as e:
        return {"unavailable": f"codegrapher status failed: {scrub_home_path(str(e))}"}
    out = (proc.stdout or "") + (proc.stderr or "")
    indexed = "not initialized" not in out.lower() and proc.returncode == 0
    return {
        "checked_repo": scrub_home_path(str(repo_path)),
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
    pipe_filter_totals: Counter = Counter()  # NM2: pipe-downstream segments, kept apart from the verb table
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
                                 adoption_agg=adoption_agg, pipe_filter_totals=pipe_filter_totals)
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
                merge_bucket(global_day_usage[(day, fam_key, "main")], bucket)

    agents = []
    for p, source in discover_subagent_files(home):
        agent_id = os.path.basename(p).replace("agent-", "").replace(".jsonl", "").replace(".output", "")
        a = process_agent_transcript(p, since, until, source)
        if not a:
            continue
        agents.append(a)
        fam = model_family(a.model)
        # minor #3: book each day's usage under the day it actually
        # happened, not unconditionally under the agent's first day.
        for day, bucket in a.day_usage.items():
            merge_bucket(global_day_usage[(day, fam, "subagent")], bucket)
        # second pass over the same file for tool-call / Bash stats, tagged role="subagent"
        process_transcript(p, since, until, "subagent", agent_id, tool_agg,
                            bash_verb_totals, bash_subverb_totals, long_bash,
                            autobg_total, autobg_timeout_total,
                            dispatch_targets, resume_targets,
                            cpu_heavy_counter, loop_events, pipe_counter, seq_miner,
                            adoption_agg=adoption_agg, default_model_family=fam,
                            pipe_filter_totals=pipe_filter_totals)

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
    agent_durations = [
        (a.last_ts - a.first_ts).total_seconds() for a in agents if a.first_ts and a.last_ts
    ]

    fleet_repos: set = set()
    wb_stats = process_wb_worklogs(wb_state_dirs, since, until, raw_dir, home=home, offline=offline,
                                    repos_out=fleet_repos)
    gh_stats = process_github(repo, since, until, raw_dir, offline)
    # Major 2 (round 3): the fleet-wide merged-PR denominator, across every
    # repo with an in-window claim/seal -- see process_fleet_merged_prs.
    fleet_pr_stats = process_fleet_merged_prs(sorted(fleet_repos), since, until, raw_dir, offline)
    git_stats = process_git_log(git_repo_path, since, until)
    tools_section = build_tools_section(tool_agg)
    bash_section = build_bash_section(bash_verb_totals, bash_subverb_totals, long_bash,
                                       autobg_total, autobg_timeout_total, cpu_heavy_counter,
                                       loop_events, pipe_counter, seq_miner,
                                       pipe_filter_totals=pipe_filter_totals)
    # M1: stalls classified as tool_running / idle_until_resume /
    # waiting_notification, for BOTH the main loop (previously collected
    # but dropped) and subagents (previously a single noisy >5min-gap
    # count where the "waiting" figure was always 0).
    stalls_section = build_stalls_section(loop_events)
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
        # minor #9: the label must include the MODEL, not just role/tool --
        # otherwise two distinct offenders (e.g. subagent/Bash under sonnet
        # and under opus) render as identical-looking duplicate rows.
        top_candidates.append({
            "pattern": f"tool result size:{off['role']}/{off['model']}/{off['tool']}",
            "estimated_tokens": off["estimated_total_tokens"],
            "count": off["calls"],
            "kind": "context_bloat",
        })

    # minor #9: the OLD sort key summed raw token counts, wall-clock
    # minutes*1000, wall-clock hours*60000 and call counts*10 into one
    # number -- arbitrary weights across incompatible units (a raw token
    # count is not commensurable with a hand-picked "hours are worth
    # 60000 points" constant). Rank honestly instead: group by `kind`
    # first (tokens -- the only kind with a real dollar figure -- ranks
    # highest, in a fixed, documented priority order), then within each
    # kind by that kind's own natural magnitude. No cross-unit blending.
    _TOP10_KIND_PRIORITY = {"tokens": 0, "context_bloat": 1, "wall_clock": 2, "tool_pattern": 3}

    def _magnitude(c):
        kind = c.get("kind")
        if kind == "tokens":
            return c.get("estimated_usd", 0) or 0
        if kind == "context_bloat":
            return c.get("estimated_tokens", 0) or 0
        if kind == "wall_clock":
            return (c.get("wall_clock_hours", 0) or 0) * 3600 + (c.get("wall_clock_minutes", 0) or 0) * 60
        return c.get("count", 0) or 0

    def sort_key(c):
        return (-_TOP10_KIND_PRIORITY.get(c.get("kind"), 99), _magnitude(c))
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
    if isinstance(fleet_pr_stats, dict) and "unavailable" in fleet_pr_stats:
        unavailable.append({"metric": "fleet-wide merged PRs (headline usd_per_fleet_merged_pr)",
                             "reason": fleet_pr_stats["unavailable"]})
    unavailable.append({
        "metric": "per-PR lint-vs-test failure rounds and review/fix rounds",
        "reason": "GitHub exposes run-level conclusion only; attributing a failed run to "
                   "lint vs test vs review requires reading job/step names or logs, which "
                   "this pass does not fetch to keep it lightweight and metrics-only",
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
    # minor #2 (report 9d): result size per subcommand IS reported now
    # (adoption.direct_use.cli_result_sizes_by_role_model_tool ->
    # result_size_by_subcommand); these two remain out of scope.
    unavailable.append({
        "metric": "specscore/codegrapher CLI exit code per direct call",
        "reason": "direct_cli detection classifies a Bash invocation by its verb/subcommand "
                   "only; the tool_result block for a Bash call does not carry the process "
                   "exit code, only stdout/stderr content and an is_error flag, so a per-call "
                   "exit code cannot be attributed without parsing command output",
    })
    unavailable.append({
        "metric": "adoption stats per session (not just per role/model)",
        "reason": "AdoptionAgg aggregates directly to (role, model family, ...) keys across "
                   "the whole window; adding session_id to every key would multiply the size "
                   "of every adoption counter/SizeStats and was not done for this pass",
    })
    if isinstance(codex_stats, dict) and "unavailable" in codex_stats:
        unavailable.append({"metric": "Codex CLI token usage", "reason": codex_stats["unavailable"]})

    # "Is SUMMARY.md useful a week later?" -- a reviewer needs the total
    # before the unavailable list buries it. Computed once here, rendered
    # first (metrics.json keeps `unavailable` last too, moved below).
    headline_section = build_headline_section(
        since, until, repo, tokens_main_by_model, tokens_subagent_by_model,
        total_cost, stalls_section, gh_stats, wb_stats, fleet_pr_stats,
    )

    metrics = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "repo": repo,
        "headline": headline_section,
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
            "price_table_as_of": PRICE_TABLE_AS_OF,
            "legacy_price_prefixes": sorted(LEGACY_PRICE_PREFIXES),
        },
        "orchestrator": {
            "sessions": len(sessions),
            "turns_total": turns_total,
            "turns_per_session_median": statistics.median([s.turns for s in sessions]) if sessions else None,
            "compactions_total": compactions_total,
            "context_growth_by_session": ctx_growth,
            # M1: main-loop stalls were collected but silently dropped before.
            "stalls": stalls_section["main"],
        },
        "subagents": {
            "count_by_model": dict(agents_by_model),
            "dispatches_by_subagent_type": dict(dispatch_targets),
            "resumes_via_send_message": dict(resume_targets),
            "resume_events_total": sum(resume_targets.values()),
            "wall_clock_hours_sum": round(sum(agent_durations) / 3600.0, 2) if agent_durations else None,
            "wall_clock_hours_median": round(statistics.median(agent_durations) / 3600.0, 2) if agent_durations else None,
            "tool_uses_median": statistics.median([a.tool_uses for a in agents]) if agents else None,
            # M1: replaces the old single noisy >5min-gap count (and the
            # always-0 "ending in waiting" figure) with a real breakdown of
            # what actually closed each gap.
            "stalls": stalls_section["subagent"],
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
        "fleet_merged_prs": fleet_pr_stats,
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

    # "Is SUMMARY.md useful a week later?" -- the headline is the FIRST
    # thing on the page, not buried after a 12-item unavailable list.
    h = metrics.get("headline") or {}
    lines.append("## Headline")
    lines.append("")
    lines.append(f"- **Total: ${h.get('total_usd')}** (${h.get('usd_per_day')}/day) "
                  f"-- main ${h.get('usd_main')}, subagent ${h.get('usd_subagent')}")
    cr_share = h.get("cache_read_share_of_usd")
    lines.append(f"- Cache-read share of total $: {cr_share * 100:.1f}%" if cr_share is not None
                  else "- Cache-read share of total $: n/a (no cost this window)")
    lines.append(f"- Total output tokens: {h.get('output_tokens_total', 0):,}")
    # Major 2 (round 3): two distinct scopes, never blended -- (a) is
    # fleet-wide cost over fleet-wide merged PRs, (b) is a plain per-repo
    # count. See build_headline_section's docstring.
    upp = h.get("usd_per_fleet_merged_pr")
    lines.append(f"- $ per merged PR (fleet): {('$' + str(upp)) if upp is not None else 'n/a'} "
                  f"({h.get('fleet_merged_prs_in_window')} merged across "
                  f"{h.get('fleet_repos_queried')} repos with in-window activity)")
    repo_name = metrics.get("repo", "?")
    repo_merged = h.get(f"{repo_name}_merged_prs_in_window")
    lines.append(f"- `{repo_name}` PRs merged in window: {repo_merged if repo_merged is not None else 'n/a'}")
    upw = h.get("usd_per_sealed_worktree")
    lines.append(f"- $ per sealed worktree: {('$' + str(upw)) if upw is not None else 'n/a'} "
                  f"({h.get('sealed_worktrees_in_window')} sealed in window)")
    sm = h.get("stall_minutes_by_kind", {})
    lines.append(f"- Stall minutes by kind -- main: {sm.get('main', {})}, subagent: {sm.get('subagent', {})}")
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
    ost = o["stalls"]
    lines.append(f"- main-loop stalls (>5min gap): {ost['total']} -- by kind: {ost['by_kind']}")
    lines.append("")

    lines.append("## Subagents")
    lines.append("")
    s = metrics["subagents"]
    lines.append(f"- count by model: {s['count_by_model']}")
    lines.append(f"- dispatches by subagent type: {s['dispatches_by_subagent_type']}")
    lines.append(f"- resumes via SendMessage: {s['resume_events_total']}")
    lines.append(f"- wall-clock hours (sum / median): {s['wall_clock_hours_sum']} / {s['wall_clock_hours_median']}")
    sst = s["stalls"]
    lines.append(f"- stalls (>5min gap): {sst['total']} -- by kind: {sst['by_kind']}")
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
    lines.append(f"- grep-to-read chains detected (each a probable CodeGrapher-substitutable "
                  f"pair): {mo['grep_to_read_chains_by_role_model']}")
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

    lines.append(f"## Pipeline and CI ({metrics.get('repo', '?')})")
    lines.append("")
    p = metrics["pipeline_and_ci"]
    if "unavailable" in p:
        lines.append(f"- unavailable: {p['unavailable']}")
    else:
        prs = p.get("prs", {})
        lines.append(f"- PRs created in window: {prs.get('created_in_window')}, "
                      f"of those already merged: {prs.get('merged')}, "
                      f"closed unmerged: {prs.get('closed_unmerged')}, still open: {prs.get('still_open')}")
        # Major 2 (round 3): the honest MERGED-in-window count (via
        # `merged:`), distinct from "created in window, merged" above.
        lines.append(f"- PRs actually merged in window (via `merged:`): {prs.get('merged_in_window')}")
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

    lines.append(f"## git log ({metrics.get('repo', '?')})")
    lines.append("")
    g = metrics["git_log"]
    if "unavailable" in g:
        lines.append(f"- unavailable: {g['unavailable']}")
    else:
        lines.append(f"- commits: {g['commits']}, merge commits: {g['merge_commits']}, PR references: {g['pr_references']}")
    lines.append("")

    # "Unavailable" goes LAST -- it's an input to the logging-gap analysis,
    # not the thing a reviewer needs first (see "Headline" at the top).
    lines.append("## Unavailable metrics (input to the logging-gap analysis)")
    lines.append("")
    for u in metrics["unavailable"]:
        lines.append(f"- **{u['metric']}**: {u['reason']}")
    lines.append("")

    return "\n".join(lines)


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def path_is_inside_git_repo(path: Path) -> bool:
    """Minor #10: refuse an --out path inside a git repo (a `.git` dir or
    worktree `.git` file anywhere from `path` up to the filesystem root) --
    this pack must never be committed."""
    p = path.resolve()
    for candidate in [p, *p.parents]:
        if (candidate / ".git").exists():
            return True
    return False


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
    if path_is_inside_git_repo(out_dir):
        eprint(f"error: --out {args.out} is inside a git repository; point it at a scratch "
               f"or reports directory outside any repo (e.g. ~/.wb/reports/<name>/) -- this "
               f"pack must never be committed")
        return 2
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
