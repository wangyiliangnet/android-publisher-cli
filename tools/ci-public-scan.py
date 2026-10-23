#!/usr/bin/env python3
"""Fail closed before public CI/build; report categories and paths, never matches."""
import os
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

EMAIL = re.compile(r"(?<![A-Za-z0-9/])[A-Za-z0-9._%+\-]+@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})")
METADATA_EMAIL = re.compile(r"[A-Za-z0-9._%+\-]+@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})")
USER_PATH = re.compile(r"/(?:Users|home)/([A-Za-z][A-Za-z0-9_.-]*)/")
PAGE = re.compile(r"page://page_[0-9a-f]{12,}", re.IGNORECASE)
BLOCKED_SUFFIXES = {".apk", ".aab", ".p12", ".pfx", ".jks", ".keystore", ".pem", ".key", ".sqlite", ".sqlite3", ".db"}
SAFE_USERS = {"example", "runner", "USER", "username", "colleague"}


def reserved_email(domain):
    domain = domain.lower()
    return domain in {"example.com", "example.org", "example.net", "users.noreply.github.com"} or domain.endswith((".invalid", ".example", ".test"))


def valid_author(email):
    return re.fullmatch(r"[A-Za-z0-9.+_\[\]-]+@users\.noreply\.github\.com", email) is not None


def valid_identity(name, email):
    return valid_author(email) or (name == "GitHub" and email == "noreply@github.com")


def valid_metadata(author, committer, author_name="", committer_name=""):
    return valid_identity(author_name, author) and valid_identity(committer_name, committer)


def text_issues(data, *, metadata=False):
    issues = []
    if b"\0" in data:
        issues.append("binary content")
        return issues
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        return issues + ["non-UTF-8 content"]
    email_pattern = METADATA_EMAIL if metadata else EMAIL
    if any(not reserved_email(m.group(1)) and m.group(0) != "noreply@github.com" for m in email_pattern.finditer(text)):
        issues.append("non-example personal email")
    if any(m.group(1) not in SAFE_USERS for m in USER_PATH.finditer(text)):
        issues.append("personal absolute home path")
    if PAGE.search(text):
        issues.append("private Page reference")
    return issues


def file_issues(path, data):
    issues = text_issues(data)
    if path.suffix.lower() in BLOCKED_SUFFIXES or path.name.startswith((".env", "state.sqlite")):
        issues.append("credential, application, or runtime file")
    return issues


def inspection_environment():
    # Always inspect stored objects, even when a caller has enabled local
    # replacement refs. Gitleaks inherits this setting for its own Git children.
    return {**os.environ, "GIT_NO_REPLACE_OBJECTS": "1"}


def git_output(root, *args):
    # A malformed ref can itself contain private text; never echo Git stderr.
    return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.DEVNULL, env=inspection_environment())


def identity(data, field):
    headers = data.split(b"\n\n", 1)[0].splitlines()
    values = [line for line in headers if line.startswith(field.encode() + b" ")]
    if len(values) != 1:
        return "", ""
    match = re.fullmatch(field.encode() + rb" ([^\n]*) <([^<>\n]+)> -?[0-9]+ [+-][0-9]{4}", values[0])
    return tuple(part.decode("utf-8", errors="replace") for part in match.groups()) if match else ("", "")


def commit_privacy_view(data):
    """Ignore only the known bot's exact official final sign-off for privacy.

    The original record is retained unchanged for Gitleaks. This is not a domain
    allowance and does not apply to source, tags, refs, or other message text.
    """
    bot = ("dependabot[bot]", "49699333+dependabot[bot]@users.noreply.github.com")
    if identity(data, "author") != bot:
        return data
    committer_name, committer_email = identity(data, "committer")
    if not valid_metadata(bot[1], committer_email, bot[0], committer_name):
        return data
    headers, separator, message = data.partition(b"\n\n")
    if not separator:
        return data
    signoff = b"Signed-off-by: dependabot[bot] <" + b"support" + b"@" + b"github.com>"
    # Require a final trailer paragraph and exactly one byte-for-byte sign-off.
    # Whitespace/case variants and an email embedded in ordinary prose stay gated.
    body, paragraph, trailers = message.removesuffix(b"\n").rpartition(b"\n\n")
    lines = trailers.split(b"\n")
    if not paragraph or lines.count(signoff) != 1:
        return data
    if not all(re.fullmatch(rb"[A-Za-z0-9-]+: [^\r\n]+", line) for line in lines):
        return data
    safe_trailers = b"\n".join(line for line in lines if line != signoff)
    return headers + separator + body + paragraph + safe_trailers + b"\n"


def metadata_records(root):
    """Collect reachable commits, refs, annotated tags, and historical path names.

    Gitleaks' git mode primarily scans patches; these separate source records make
    names/messages/ref names/tag headers and messages subject to both gates too.
    Labels are fixed categories or object IDs, never metadata-derived display text.
    """
    records = []
    failures = []
    pending_tags = []
    unsupported_refs = False
    privacy_views = {}
    refs = git_output(root, "for-each-ref", "--format=%(refname)%00%(objecttype)%00%(objectname)").splitlines()
    for index, record in enumerate(refs):
        name, kind, oid = record.split(b"\0")
        label = f"Git ref {index + 1}"
        records.append((label, b"refname: " + name + b"\n"))
        if name.startswith(b"refs/replace/"):
            failures.append((label, "replacement refs are forbidden"))
        if kind == b"tag":
            pending_tags.append(oid.decode("ascii"))
        elif kind != b"commit":
            failures.append((label, "ref must resolve through tags to a commit"))
            unsupported_refs = True
    seen_tags = set()
    while pending_tags:
        oid = pending_tags.pop()
        if oid in seen_tags:
            continue
        seen_tags.add(oid)
        label = f"Git annotated tag {oid}"
        if int(git_output(root, "cat-file", "-s", oid)) > 2 * 1024 * 1024:
            failures.append((label, "unexpected large Git metadata"))
            unsupported_refs = True
            continue
        data = git_output(root, "cat-file", "tag", oid)
        records.append((label, data))
        if not valid_identity(*identity(data, "tagger")):
            failures.append((label, "tagger must use GitHub noreply or the exact GitHub system identity"))
        headers = data.split(b"\n\n", 1)[0].splitlines()
        targets = [line.removeprefix(b"object ") for line in headers if line.startswith(b"object ")]
        if len(targets) != 1 or not re.fullmatch(rb"[0-9a-f]{40}(?:[0-9a-f]{24})?", targets[0]):
            failures.append((label, "invalid annotated tag target"))
            unsupported_refs = True
            continue
        target = targets[0].decode("ascii")
        kind = git_output(root, "cat-file", "-t", target).strip()
        if kind == b"tag":
            pending_tags.append(target)
        elif kind != b"commit":
            failures.append((label, "tag must resolve through tags to a commit"))
            unsupported_refs = True
    # Unsupported refs already block publication; don't ask rev-list to interpret
    # arbitrary blob/tree refs (whose diagnostic could contain the unsafe ref).
    if not unsupported_refs:
        paths = set()
        commits = git_output(root, "rev-list", "--all").decode("ascii").splitlines()
        for oid in commits:
            label = f"Git commit {oid}"
            if int(git_output(root, "cat-file", "-s", oid)) > 2 * 1024 * 1024:
                failures.append((label, "unexpected large Git metadata"))
                continue
            data = git_output(root, "cat-file", "commit", oid)
            records.append((label, data))
            privacy_views[label] = commit_privacy_view(data)
            author_name, author_email = identity(data, "author")
            committer_name, committer_email = identity(data, "committer")
            if not valid_metadata(author_email, committer_email, author_name, committer_name):
                failures.append((label, "use GitHub noreply or the exact GitHub system identity"))
            entries = git_output(root, "ls-tree", "-r", "-z", "--full-tree", oid).split(b"\0")
            paths.update(entry.split(b"\t", 1)[1] for entry in entries if entry)
        for index, path in enumerate(sorted(paths)):
            records.append((f"Git historical path {index + 1}", b"path: " + path + b"\n"))
    for label, data in records:
        failures.extend((label, issue) for issue in text_issues(privacy_views.get(label, data), metadata=True))
    return records, failures


def history_issues(root):
    """Inspect every reachable blob/path pair, including deleted private text."""
    commits = git_output(root, "rev-list", "--all").decode("ascii").splitlines()
    objects = set()
    failures = []
    for commit in commits:
        entries = git_output(root, "ls-tree", "-r", "-z", "--full-tree", commit).split(b"\0")
        for entry in entries:
            if not entry:
                continue
            meta, name = entry.split(b"\t", 1)
            mode, kind, oid = meta.decode("ascii").split()
            path = os.fsdecode(name)
            if kind != "blob" or mode not in {"100644", "100755"}:
                failures.append((path, "unexpected historical object type"))
                continue
            objects.add((oid, path))
    proc = subprocess.Popen(["git", "-C", str(root), "cat-file", "--batch"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=inspection_environment())
    try:
        for oid, path in sorted(objects):
            proc.stdin.write((oid + "\n").encode())
            proc.stdin.flush()
            header = proc.stdout.readline().decode("ascii").split()
            if len(header) != 3 or header[1] != "blob":
                raise RuntimeError("Cannot inspect historical source blob")
            size = int(header[2])
            if size > 2 * 1024 * 1024:
                failures.append((path, "unexpected large historical source file"))
            remaining = size
            chunks = []
            while remaining:
                data = proc.stdout.read(min(remaining, 65536))
                if not data:
                    raise RuntimeError("Truncated historical source blob")
                if size <= 2 * 1024 * 1024:
                    chunks.append(data)
                remaining -= len(data)
            if proc.stdout.read(1) != b"\n":
                raise RuntimeError("Cannot delimit historical source blob")
            if size <= 2 * 1024 * 1024:
                failures.extend((path, "historical " + issue) for issue in file_issues(Path(path), b"".join(chunks)))
    finally:
        proc.stdin.close()
        proc.stdout.close()
        proc.wait()
    return failures


def run():
    root = Path.cwd()
    entries = git_output(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").split(b"\0")
    paths = sorted({os.fsdecode(entry) for entry in entries if entry})
    failures = []
    for value in paths:
        path = Path(value)
        full = root / path
        if path.is_absolute() or ".." in path.parts or full.is_symlink() or not full.is_file():
            failures.append((value, "unexpected file type or path"))
            continue
        if full.stat().st_size > 2 * 1024 * 1024:
            failures.append((value, "unexpected large source file"))
            continue
        failures.extend((value, issue) for issue in file_issues(path, full.read_bytes()))
    metadata, metadata_failures = metadata_records(root)
    failures.extend(metadata_failures)
    if not metadata_failures:
        failures.extend(history_issues(root))
    if failures:
        for path, category in failures:
            print(f"Public-source gate: {category}: {json.dumps(path)}", file=sys.stderr)
        return 1
    scanner = shutil.which("gitleaks")
    if not scanner:
        print("Public-source gate requires the pinned Gitleaks tool.", file=sys.stderr)
        return 1
    with tempfile.TemporaryDirectory(prefix="publisher-public-scan-", dir=os.environ.get("RUNNER_TEMP")) as temp:
        base = Path(temp)
        export = base / "source"
        export.mkdir()
        for value in paths:
            dest = export / value
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(root / value, dest)
        metadata_export = base / "metadata"
        metadata_export.mkdir()
        for index, (_, data) in enumerate(metadata):
            (metadata_export / f"record-{index + 1}.txt").write_bytes(data)
        config = root / ".gitleaks.toml"
        common = [scanner, "--redact=100", "--no-banner", "--config", str(config)]
        for label, args in [("current source", ["dir", str(export)]), ("public history", ["git", "--log-opts=--all", str(root)]), ("Git metadata", ["dir", str(metadata_export)])]:
            # Never echo findings, credentials, author metadata, or raw scanner output.
            with (base / "scanner.log").open("wb") as log:
                result = subprocess.run(common + args, stdout=log, stderr=log, check=False, env=inspection_environment())
            if result.returncode:
                print(f"Gitleaks rejected {label}; inspect locally with redaction before publishing.", file=sys.stderr)
                return 1
    print(f"Public-source privacy and secret scan passed ({len(paths)} source files and all public Git history).")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(run())
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
        print("Public-source gate could not safely inspect Git source and metadata.", file=sys.stderr)
        sys.exit(1)
