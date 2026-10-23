#!/usr/bin/env python3
"""Ensure the single synthetic allowance cannot hide a different credential."""
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
    ("internal/protocol/context_test.go", fixture, 0),
    ("internal/protocol/context_test.go", fixture * 2, 0),
    ("other.go", fixture * 2, 1),
    ("internal/protocol/context_test.go", hashlib.sha256(b"synthetic-rejected-credential").hexdigest(), 1),
]
for path, secret, expected in cases:
    with tempfile.TemporaryDirectory(prefix="publisher-leak-policy-") as temporary:
        root = Path(temporary)
        target = root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text('api_key = "' + secret + '"\n')
        result = subprocess.run([scanner, "--redact=100", "--no-banner", "--config", str(config), "dir", str(root)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if result.returncode != expected:
            raise SystemExit("Gitleaks synthetic allowance regression failed; no findings were printed.")
print("Gitleaks exact path-and-secret allowance regression checks passed.")
