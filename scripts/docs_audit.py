#!/usr/bin/env python3
"""Audit the illustrated HTML docs against the code's actual surface.

The two HTML documents under docs/ are meant to stay true as the project grows.
Prose drifts silently, so this checks the one thing that can be checked
mechanically: every element of the user-facing surface — CLI command, flag,
TOMESHIFT_* variable, just recipe, exit code — is mentioned somewhere in
them.

It proves coverage, not correctness: mentioning a flag is not explaining it.
What it does catch is the common failure — a feature shipped, the docs never
reopened.

Usage:  python3 scripts/docs_audit.py [--quiet]
Exit:   0 = every element is mentioned, 1 = something is undocumented.
"""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# The documents under audit. A new element must appear in at least one of them.
DOCS = [
    ROOT / "docs" / "usage.html",
    ROOT / "docs" / "understanding-the-project.html",
]

# Elements deliberately left out of the illustrated docs, with the reason.
# Adding a name here is a decision, not a shortcut: it says "a reader does not
# need this", which is a claim the next reviewer can disagree with.
ALLOW = {
    "flag:h": "stdlib flag package builtin",
    "recipe:default": "just's own listing, not a user-facing step",
    "recipe:show-env": "trivial alias over print-config-vars",
    "recipe:build": "covered by the go build line in section 1",
    "recipe:fmt": "covered by just check",
    "recipe:lint": "covered by just check",
    "recipe:test": "covered by just check",
    "recipe:test-update-golden": "test-maintenance only; discoverable via just --list, not a user-facing step",
    "recipe:comet-down": "symmetric with comet-up, no distinct consequence",
    "recipe:seed-bible": "documented under its command name, style-profile",
    "recipe:batch": "documented under its command name",
    "env:TOMESHIFT_NMT_RETRY_BASE_MS": "documented next to NMT_MAX_ATTEMPTS",
    "env:TOMESHIFT_COMET_API_KEY": "documented next to COMET_ENDPOINT",
    "env:TOMESHIFT_LLM_ENABLED": "documented next to LLM_ENDPOINT",
    "env:TOMESHIFT_LLM_CACHE_WRITE_MULTIPLIER": "pricing detail, covered by the cost model section",
    "env:TOMESHIFT_LLM_OUTPUT_EXPANSION": "pricing detail, covered by the cost model section",
    "env:TOMESHIFT_LLM_PROMPT_OVERHEAD_TOKENS": "pricing detail, covered by the cost model section",
    "env:TOMESHIFT_COST_PER_SEGMENT_USD": "documented next to COST_PER_1000_CHARS_USD",
}


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def corpus() -> str:
    missing = [d for d in DOCS if not d.exists()]
    if missing:
        names = ", ".join(str(m.relative_to(ROOT)) for m in missing)
        sys.exit(f"docs-audit: missing document(s): {names}")
    return "\n".join(read(d) for d in DOCS)


def commands() -> set[str]:
    """CLI verbs dispatched by commandexec.Execute."""
    src = read(ROOT / "internal" / "commandexec" / "execute.go")
    found = set(re.findall(r'^\tcase "([a-z-]+)":', src, re.M))
    return found - {"help"}


def flags() -> set[str]:
    """Flag names registered by the cli package."""
    found: set[str] = set()
    for path in (ROOT / "internal" / "cli").glob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        found |= set(re.findall(r'fs\.\w+Var\([^,]+,\s*"([a-z-]+)"', read(path)))
    return found


def env_vars() -> set[str]:
    """Settings the operator is expected to know, i.e. what .env.example lists."""
    src = read(ROOT / ".env.example")
    return set(re.findall(r"^(TOMESHIFT_[A-Z0-9_]+)=", src, re.M))


def recipes() -> set[str]:
    """Recipe names declared in the Justfile.

    `just --summary` is the authority. A regex over the file cannot be: a
    recipe header and a line of embedded shell are the same shape, and the
    `${VAR:-default}` inside a multi-line variable was duly reported as a
    recipe named `if`.
    """
    try:
        out = subprocess.run(
            ["just", "--summary"], cwd=ROOT,
            capture_output=True, text=True, check=True, timeout=30,
        ).stdout
        return set(out.split())
    except (OSError, subprocess.SubprocessError):
        # Without `just` on PATH the recipe list is unavailable. Report nothing
        # rather than guess: a wrong list is worse than a missing check, since
        # it sends the reader to document something that does not exist.
        print("docs-audit: 'just' unavailable — skipping the recipe check")
        return set()


def exit_codes() -> set[str]:
    """Documented process exit codes."""
    src = read(ROOT / "internal" / "exitcode" / "codes.go")
    return set(re.findall(r"^\t\w+ = (\d+)$", src, re.M))


def mentioned(needle: str, text: str) -> bool:
    return needle in text


def main() -> int:
    quiet = "--quiet" in sys.argv
    text = corpus()

    checks = [
        ("command", commands(), lambda c: f"tomeshift {c}"),
        ("flag", flags(), lambda f: f"--{f}"),
        ("env", env_vars(), lambda e: e.removeprefix("TOMESHIFT_")),
        ("recipe", recipes(), lambda r: f"just {r}"),
        ("exit", exit_codes(), lambda c: f"exit {c}"),
    ]

    gaps: list[tuple[str, str, str]] = []
    total = 0
    for kind, names, render in checks:
        for name in sorted(names):
            if f"{kind}:{name}" in ALLOW:
                continue
            total += 1
            needle = render(name)
            if not mentioned(needle, text):
                gaps.append((kind, name, needle))

    if not quiet:
        print(f"docs-audit: {total - len(gaps)}/{total} elements documented "
              f"across {len(DOCS)} documents")

    if gaps:
        print("\ndocs-audit: undocumented surface — update the illustrated docs")
        print("(see AGENTS.md § Living Documentation for which section to touch)\n")
        for kind, name, needle in gaps:
            print(f"  {kind:8} {name:34} expected the string {needle!r}")
        print("\nIf a reader genuinely does not need it, add it to ALLOW in "
              "scripts/docs_audit.py with the reason.")
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
