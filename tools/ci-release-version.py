#!/usr/bin/env python3
"""Validate the exact SemVer used in a manual CI preview release."""
import re
import sys

BASE = r"(?:0|[1-9][0-9]*)"
PRE = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
PATTERN = re.compile(rf"{BASE}\.{BASE}\.{BASE}(?:-{PRE}(?:\.{PRE})*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?\Z", re.ASCII)


def valid(version):
    return len(version) <= 100 and PATTERN.fullmatch(version) is not None


def self_test():
    good = ["0.1.0", "1.2.3-beta.1", "1.2.3-1a.0+build.5", "1.2.3+001"]
    bad = ["", "v1.2.3", "01.2.3", "1.2", "1.2.3-01", "1.2.3-", "1.2.3+", "1.2.3\n", "1.2.3;id", "1.2.3-$(id)", "1.2.3/evil", "１.2.3", "1.2.3-" + "a" * 100]
    assert all(valid(v) for v in good)
    assert not any(valid(v) for v in bad)


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-test"]:
        self_test()
    elif len(sys.argv) == 2 and valid(sys.argv[1]):
        print(sys.argv[1])
    else:
        print("Use a SemVer without the leading v (maximum 100 ASCII characters).", file=sys.stderr)
        sys.exit(2)
