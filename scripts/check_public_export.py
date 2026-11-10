#!/usr/bin/env python3
"""Conservative source-release checks; diagnostics never print matched values.

This is a focused privacy guard, not proof that arbitrary files contain no
secrets. Review all newly added source, images, and Git metadata before release.
"""

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
SKIP = {".git", "bin", "dist", "__pycache__"}
PRIVATE_DIRS = {".codex", ".codexpool", "accounts", "locks", "cache",
                "runtime-probe", "desktop-activation", ".ssh", ".aws"}
PRIVATE_FILES = {"auth.json", "registry.json", "usage.json", ".DS_Store"}
TEXT_EXTENSIONS = {".go", ".md", ".sh", ".mod", ".sum", ".json", ".ts",
                   ".js", ".css", ".html", ".py", ".yml", ".yaml", ".txt"}
TEXT_NAMES = {"LICENSE", ".gitignore", "NOTICE"}
RULES = {
    "personal-home-path": re.compile(r"(?<![A-Za-z0-9_])/(?:Users|home)/[A-Za-z0-9_.-]+/"),
    "windows-home-path": re.compile(r"[A-Za-z]:\\Users\\[A-Za-z0-9_.-]+\\"),
    "private-key": re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    "provider-token": re.compile(r"(?<![\w-])(?:sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[A-Z0-9]{16})(?![\w-])"),
    "jwt": re.compile(r"\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\b"),
    "url-credentials": re.compile(r"https?://[^\s/@]+:[^\s/@]+@"),
}
EMAIL = re.compile(r"[A-Za-z0-9_.+%-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,})")


def paths(tracked):
    if tracked:
        raw = subprocess.check_output(["git", "ls-files", "--cached", "-z"], cwd=ROOT)
        return [ROOT / os.fsdecode(p) for p in raw.split(b"\0") if p]
    result = []
    for base, dirs, files in os.walk(ROOT, followlinks=False):
        result.extend(Path(base) / d for d in dirs if (Path(base) / d).is_symlink())
        dirs[:] = [d for d in dirs if d not in SKIP and not (Path(base) / d).is_symlink()]
        result.extend(Path(base) / f for f in files)
    return sorted(result)


def check(tracked=False):
    findings = []
    checked = 0
    for path in paths(tracked):
        rel = path.relative_to(ROOT)
        if path.is_symlink():
            findings.append((rel, 0, "symlink"))
            continue
        if (set(rel.parts[:-1]) & PRIVATE_DIRS or path.name in PRIVATE_FILES
                or path.name.startswith((".env", "._"))
                or path.suffix.lower() in {".pem", ".key", ".p12", ".pfx", ".db", ".log", ".jsonl", ".zip", ".gz"}
                or path.name.endswith("-auth.json")):
            findings.append((rel, 0, "private-or-generated-file"))
            continue
        if path.suffix not in TEXT_EXTENSIONS and path.name not in TEXT_NAMES:
            findings.append((rel, 0, "unreviewed-file-type"))
            continue
        try:
            content = path.read_text(encoding="utf-8")
        except (UnicodeError, OSError):
            findings.append((rel, 0, "not-readable-utf8"))
            continue
        checked += 1
        for number, line in enumerate(content.splitlines(), 1):
            for name, pattern in RULES.items():
                if pattern.search(line):
                    findings.append((rel, number, name))
            for match in EMAIL.finditer(line):
                domain = match.group(1).lower()
                if domain not in {"example.com", "example.org", "example.net"} and not domain.endswith((".invalid", ".example")):
                    findings.append((rel, number, "non-example-email"))
    for rel, line, rule in findings:
        print(f"FAIL {rel}:{line}: {rule}")
    print(f"Privacy source check: {checked} text files, {len(findings)} findings.")
    return 1 if findings else 0


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tracked", action="store_true", help="Check Git's complete staged/tracked file list, including ignored tracked files")
    sys.exit(check(parser.parse_args().tracked))
