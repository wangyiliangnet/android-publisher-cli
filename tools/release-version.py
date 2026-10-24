#!/usr/bin/env python3
"""Prepare a reviewed npm version or verify the version selected for publishing."""
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
BASE = r"(?:0|[1-9][0-9]*)"
PRE = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
PATTERN = re.compile(rf"({BASE})\.({BASE})\.({BASE})(?:-({PRE}(?:\.{PRE})*))?\Z", re.ASCII)


def parse(version):
    match = PATTERN.fullmatch(version) if len(version) <= 100 else None
    if not match:
        raise ValueError("Use a SemVer without v or build metadata (maximum 100 ASCII characters).")
    major, minor, patch, prerelease = match.groups()
    return (int(major), int(minor), int(patch)), prerelease.split(".") if prerelease else []


def greater(new, old):
    new_base, new_pre = parse(new)
    old_base, old_pre = parse(old)
    if new_base != old_base:
        return new_base > old_base
    if not new_pre or not old_pre:
        return bool(old_pre) and not new_pre
    for left, right in zip(new_pre, old_pre):
        left_num, right_num = left.isdecimal(), right.isdecimal()
        if left_num and right_num:
            if int(left) != int(right):
                return int(left) > int(right)
        elif left_num != right_num:
            return not left_num
        elif left != right:
            return left > right
    return len(new_pre) > len(old_pre)


def manifest_version():
    manifest = json.loads((ROOT / "package.json").read_text())
    lock = json.loads((ROOT / "package-lock.json").read_text())
    version = manifest["version"]
    if manifest["name"] != "android-publisher-cli" or manifest.get("private") or \
            manifest["bin"] != {"android-release": "./build/cli.js"} or \
            lock["version"] != version or lock["packages"][""]["version"] != version or \
            lock["packages"][""]["name"] != manifest["name"] or \
            lock["packages"][""]["bin"] != {"android-release": "build/cli.js"}:
        raise ValueError("Package metadata, CLI bin or lockfile is inconsistent.")
    parse(version)
    return version


def prepare(version, note):
    current = manifest_version()
    if not greater(version, current):
        raise ValueError(f"New version must be greater than {current}.")
    if not 10 <= len(note.strip()) <= 500 or "\n" in note or "\r" in note or \
            any(ord(char) < 32 or ord(char) == 127 for char in note):
        raise ValueError("Changelog note must be one line of 10–500 visible characters.")
    changelog = ROOT / "CHANGELOG.md"
    content = changelog.read_text()
    if not content.startswith("# 更新记录\n\n") or f"\n## {version}\n" in content:
        raise ValueError("Changelog heading is missing or the version already exists.")
    subprocess.run(["npm", "version", version, "--no-git-tag-version", "--ignore-scripts"], cwd=ROOT, check=True)
    if manifest_version() != version:
        raise ValueError("npm version did not update both manifests.")
    changelog.write_text(f"# 更新记录\n\n## {version}\n\n- {note.strip()}\n\n" + content[len("# 更新记录\n\n"):])


def check_tag(tag):
    version = manifest_version()
    if tag != f"v{version}":
        raise ValueError(f"Tag {tag} does not match package version v{version}.")
    if f"\n## {version}\n" not in (ROOT / "CHANGELOG.md").read_text():
        raise ValueError(f"Changelog has no entry for {version}.")
    return "next" if parse(version)[1] else "latest"


def self_test():
    for bad in ["", "v1.2.3", "01.2.3", "1.2", "1.2.3-01", "1.2.3-", "1.2.3+build", "1.2.3\n", "１.2.3", "1.2.3-" + "a" * 100]:
        try:
            parse(bad)
        except ValueError:
            continue
        raise AssertionError(bad)
    for old, new in [("0.2.0-dev", "0.2.0"), ("1.0.0-beta.9", "1.0.0-beta.10"), ("1.0.0-1", "1.0.0-rc"), ("1.0.0", "1.0.1")]:
        assert greater(new, old)
        assert not greater(old, new)
    assert not greater("1.0.0", "1.0.0")
    assert not greater("1.0.0-beta", "1.0.0-dev")


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["--self-test"]:
            self_test()
        elif len(sys.argv) == 4 and sys.argv[1] == "--prepare":
            prepare(sys.argv[2], sys.argv[3])
        elif len(sys.argv) == 3 and sys.argv[1] == "--check-tag":
            print(check_tag(sys.argv[2]))
        else:
            raise ValueError("Usage: release-version.py --self-test | --prepare VERSION NOTE | --check-tag TAG")
    except (ValueError, subprocess.CalledProcessError) as error:
        print(error, file=sys.stderr)
        sys.exit(2)
