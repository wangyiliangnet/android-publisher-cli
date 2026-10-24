#!/usr/bin/env python3
"""Ensure no source path bypasses the default secret rules."""
import hashlib
from pathlib import Path
import shutil
import subprocess
import tempfile

scanner = shutil.which("gitleaks")
if not scanner:
    raise SystemExit("Pinned Gitleaks is required for its allowance regression checks.")
config = Path(".gitleaks.toml").resolve()
fixture = "01234567" + "89abcdef"
cases = [
    ("src/model.ts", fixture, 1),
    ("test/synthetic.test.mjs", fixture * 2, 1),
    ("other.go", fixture * 2, 1),
    ("src/model.ts", hashlib.sha256(b"synthetic-rejected-credential").hexdigest(), 1),
]
for path, secret, expected in cases:
    with tempfile.TemporaryDirectory(prefix="publisher-leak-policy-") as temporary:
        root = Path(temporary)
        target = root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text('api_key = "' + secret + '"\n')
        result = subprocess.run([scanner, "--redact=100", "--no-banner", "--config", str(config), "dir", str(root)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if result.returncode != expected:
            raise SystemExit("Gitleaks path regression failed; no findings were printed.")
print("Gitleaks default rules apply to every tested source path.")
