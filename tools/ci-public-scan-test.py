#!/usr/bin/env python3
"""Synthetic regression checks for the public-source privacy gate."""
import importlib.util
from pathlib import Path
import unittest
from unittest import mock
import sys
import subprocess
import tempfile
import contextlib
import io
import hashlib
import os
import shutil

sys.dont_write_bytecode = True

SPEC = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("ci-public-scan.py"))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)
CONFIG = Path(__file__).resolve().parent.parent / ".gitleaks.toml"
BOT_NAME = "dependabot[bot]"
BOT_EMAIL = "49699333+dependabot[bot]@users.noreply.github.com"
BOT_SIGNOFF = "Signed-off-by: dependabot[bot] <" + "support" + "@" + "github.com>"


@contextlib.contextmanager
def fixture_repository():
    with tempfile.TemporaryDirectory(prefix="public-metadata-fixture-") as temporary:
        root = Path(temporary)
        def git(*args, **kwargs):
            kwargs["env"] = {**kwargs.pop("env", os.environ), "GIT_NO_REPLACE_OBJECTS": "1"}
            return subprocess.run(["git", "-C", str(root), *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, **kwargs)
        git("init", "--initial-branch=main")
        git("config", "user.name", "Fixture")
        git("config", "user.email", "123+fixture@users.noreply.github.com")
        (root / "README.md").write_text("Public synthetic fixture.\n")
        shutil.copyfile(CONFIG, root / ".gitleaks.toml")
        git("add", ".")
        git("commit", "-m", "Public synthetic fixture")
        yield root, git


def invoke_gate(root):
    stdout, stderr = io.StringIO(), io.StringIO()
    with contextlib.chdir(root), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        result = gate.run()
    return result, stdout.getvalue() + stderr.getvalue()


def bot_commit(git, message, *, name=BOT_NAME, email=BOT_EMAIL):
    git("commit", "--allow-empty", "--cleanup=verbatim", "-m", message, env={**os.environ, "GIT_AUTHOR_NAME": name, "GIT_AUTHOR_EMAIL": email, "GIT_COMMITTER_NAME": "GitHub", "GIT_COMMITTER_EMAIL": "noreply@github.com"})


class PrivacyGateTest(unittest.TestCase):
    def test_reserved_examples_and_url_userinfo_fixture(self):
        self.assertEqual(gate.file_issues(Path("fixture.go"), b"synthetic@example.invalid /Users/example/project https://token@api.open.oppomobile.com"), [])

    def test_personal_email_and_home_path(self):
        email = ("person" + "@" + "private" + ".com").encode()
        home = ("/Users/" + "private-person/project").encode()
        self.assertIn("non-example personal email", gate.file_issues(Path("doc.md"), email))
        self.assertIn("personal absolute home path", gate.file_issues(Path("doc.md"), home))

    def test_author_and_committer_email_policy(self):
        author = "123+fixture@users.noreply.github.com"
        bot = "123+dependabot[bot]@users.noreply.github.com"
        self.assertTrue(gate.valid_metadata(author, "noreply@github.com", committer_name="GitHub"))
        self.assertTrue(gate.valid_metadata(bot, bot))
        self.assertFalse(gate.valid_metadata("noreply@github.com", author))
        self.assertTrue(gate.valid_metadata("noreply@github.com", "noreply@github.com", "GitHub", "GitHub"))
        self.assertFalse(gate.valid_metadata(author, "noreply@github.com", committer_name="Fixture"))
        self.assertFalse(gate.valid_metadata(author, "person" + "@" + "github.com"))
        self.assertEqual(gate.file_issues(Path("doc.md"), b"noreply@github.com"), [])

    def test_deleted_personal_text_is_still_rejected_in_history(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            def git(*args):
                return subprocess.run(["git", "-C", str(root), *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=gate.inspection_environment())
            git("init", "--initial-branch=main")
            git("config", "user.name", "Fixture")
            git("config", "user.email", "123+fixture@users.noreply.github.com")
            path = root / "removed.md"
            path.write_text("/Users/" + "private-person/project")
            git("add", "removed.md")
            git("commit", "-m", "Synthetic history fixture")
            path.unlink()
            git("add", "removed.md")
            git("commit", "-m", "Remove synthetic text")
            issues = gate.history_issues(root)
            self.assertIn(("removed.md", "historical personal absolute home path"), issues)

    def test_private_page_and_runtime_or_binary(self):
        page = ("page://" + "page_" + "a" * 32).encode()
        self.assertIn("private Page reference", gate.file_issues(Path("doc.md"), page))
        for path in ["app.apk", "signing.p12", ".env", "state.sqlite-wal"]:
            self.assertTrue(gate.file_issues(Path(path), b"synthetic"))
        self.assertIn("binary content", gate.file_issues(Path("renamed.txt"), b"a\0b"))

    def test_privacy_in_commit_names_messages_refs_and_tag_metadata(self):
        email = "person" + "@" + "private" + ".com"
        private_home = "/Users/" + "private-person/project"
        cases = [
            ("commit message", lambda git: git("commit", "--allow-empty", "-m", email)),
            ("author name", lambda git: git("-c", "user.name=" + email, "commit", "--allow-empty", "-m", "Public message")),
            ("committer name", lambda git: git("commit", "--allow-empty", "-m", "Public message", env={**os.environ, "GIT_COMMITTER_NAME": email})),
            ("ref name", lambda git: git("update-ref", "refs/heads/" + email, "HEAD")),
            ("tag name", lambda git: git("tag", "-a", email, "-m", "Public message")),
            ("tagger name", lambda git: git("-c", "user.name=" + email, "tag", "-a", "v1.0.0", "-m", "Public message")),
            ("tag message", lambda git: git("tag", "-a", "v1.0.0", "-m", private_home)),
        ]
        for label, change in cases:
            with self.subTest(label=label), fixture_repository() as (root, git):
                change(git)
                _, issues = gate.metadata_records(root)
                self.assertTrue(issues, "Private Git metadata escaped the privacy gate")
                result, output = invoke_gate(root)
                self.assertEqual(result, 1)
                self.assertFalse(email in output or private_home in output, "Matched metadata text was emitted")

    def test_tagger_policy_and_exact_github_system_identity(self):
        with fixture_repository() as (root, git):
            git("-c", "user.email=synthetic@example.invalid", "tag", "-a", "v1.0.0", "-m", "Public fixture")
            _, issues = gate.metadata_records(root)
            self.assertTrue(any("tagger must" in category for _, category in issues))
        with fixture_repository() as (root, git):
            git("-c", "user.name=GitHub", "-c", "user.email=noreply@github.com", "commit", "--allow-empty", "-m", "Synthetic PR merge")
            git("-c", "user.name=GitHub", "-c", "user.email=noreply@github.com", "tag", "-a", "v1.0.0", "-m", "Public fixture")
            _, issues = gate.metadata_records(root)
            self.assertEqual(issues, [])
        with fixture_repository() as (root, git):
            git("-c", "user.name=Fixture", "-c", "user.email=noreply@github.com", "tag", "-a", "v1.0.0", "-m", "Public fixture")
            _, issues = gate.metadata_records(root)
            self.assertTrue(any("tagger must" in category for _, category in issues))

    def test_nested_annotated_tag_metadata_is_not_lost_after_inner_ref_deletion(self):
        private_home = "/Users/" + "private-person/project"
        with fixture_repository() as (root, git):
            git("tag", "-a", "inner", "-m", private_home)
            git("tag", "-a", "outer", "inner", "-m", "Public fixture")
            git("tag", "-d", "inner")
            records, issues = gate.metadata_records(root)
            self.assertTrue(any(private_home.encode() in data for _, data in records))
            self.assertTrue(any(category == "personal absolute home path" for _, category in issues))

    @unittest.skipUnless(shutil.which("gitleaks"), "Pinned Gitleaks is required for metadata secret regressions")
    def test_real_gitleaks_rejects_secrets_in_every_metadata_field_without_echoing(self):
        secret = hashlib.sha256(b"synthetic-metadata-regression-credential").hexdigest()
        credential = "api_key=" + secret
        cases = [
            ("commit message", lambda git: git("commit", "--allow-empty", "-m", credential)),
            ("historical commit message", lambda git: (git("commit", "--allow-empty", "-m", credential), git("commit", "--allow-empty", "-m", "Later public message"))),
            ("author name", lambda git: git("-c", "user.name=" + credential, "commit", "--allow-empty", "-m", "Public message")),
            ("committer name", lambda git: git("commit", "--allow-empty", "-m", "Public message", env={**os.environ, "GIT_COMMITTER_NAME": credential})),
            ("ref name", lambda git: git("update-ref", "refs/heads/" + credential, "HEAD")),
            ("tag name", lambda git: git("tag", "-a", credential, "-m", "Public message")),
            ("tagger name", lambda git: git("-c", "user.name=" + credential, "tag", "-a", "v1.0.0", "-m", "Public message")),
            ("tag message", lambda git: git("tag", "-a", "v1.0.0", "-m", credential)),
            ("nested tag message", lambda git: (git("tag", "-a", "inner", "-m", credential), git("tag", "-a", "outer", "inner", "-m", "Public message"), git("tag", "-d", "inner"))),
        ]
        for label, change in cases:
            with self.subTest(label=label), fixture_repository() as (root, git):
                change(git)
                _, issues = gate.metadata_records(root)
                self.assertEqual(issues, [], "This fixture must reach the actual secret scanner")
                result, output = invoke_gate(root)
                self.assertEqual(result, 1)
                self.assertIn("Gitleaks rejected Git metadata", output)
                self.assertFalse(secret in output, "Matched metadata secret was emitted")

    @unittest.skipUnless(shutil.which("gitleaks"), "Pinned Gitleaks is required for the successful metadata fixture")
    def test_clean_metadata_and_system_merge_pass_the_complete_gate(self):
        with fixture_repository() as (root, git):
            git("-c", "user.name=GitHub", "-c", "user.email=noreply@github.com", "commit", "--allow-empty", "-m", "Synthetic PR merge")
            git("tag", "-a", "v1.0.0", "-m", "Public preview")
            result, _ = invoke_gate(root)
            self.assertEqual(result, 0)

    def test_replacement_refs_cannot_hide_original_commit_messages(self):
        email = "person" + "@" + "private" + ".com"
        secret = hashlib.sha256(b"synthetic-replaced-metadata-credential").hexdigest()
        for label, message in [("privacy", email), ("secret", "api_key=" + secret)]:
            with self.subTest(label=label), fixture_repository() as (root, git):
                git("commit", "--allow-empty", "-m", message)
                original = gate.git_output(root, "rev-parse", "HEAD").decode().strip()
                tree = gate.git_output(root, "rev-parse", "HEAD^{tree}").decode().strip()
                parent = gate.git_output(root, "rev-parse", "HEAD^").decode().strip()
                clean = gate.git_output(root, "commit-tree", tree, "-p", parent, "-m", "Public replacement message").decode().strip()
                git("replace", original, clean)
                # Even a caller explicitly enabling replacements cannot change
                # which stored commit the scanner inspects.
                with mock.patch.dict(os.environ, {"GIT_NO_REPLACE_OBJECTS": "0"}):
                    records, issues = gate.metadata_records(root)
                    raw = gate.git_output(root, "cat-file", "commit", original)
                    self.assertTrue(message.encode() in raw, "Original commit metadata was hidden")
                    self.assertTrue(any(message.encode() in data for _, data in records), "Original commit was absent from the audit")
                    self.assertTrue(any(category == "replacement refs are forbidden" for _, category in issues))
                    if label == "privacy":
                        self.assertTrue(any(category == "non-example personal email" for _, category in issues))
                    result, output = invoke_gate(root)
                self.assertEqual(result, 1)
                self.assertIn("replacement refs are forbidden", output)
                self.assertFalse(any(value in output for value in [message, secret, "refs/replace/"]), "Unsafe metadata or ref value was emitted")

    def test_replacement_refs_cannot_hide_original_blob_privacy(self):
        private_home = "/Users/" + "private-person/project"
        with fixture_repository() as (root, git):
            base = gate.git_output(root, "rev-parse", "HEAD").decode().strip()
            (root / "README.md").write_text(private_home)
            git("add", "README.md")
            git("commit", "-m", "Synthetic replaced tree")
            original = gate.git_output(root, "rev-parse", "HEAD").decode().strip()
            clean_tree = gate.git_output(root, "rev-parse", base + "^{tree}").decode().strip()
            clean = gate.git_output(root, "commit-tree", clean_tree, "-p", base, "-m", "Public replacement tree").decode().strip()
            git("replace", original, clean)
            git("checkout", base, "--", "README.md")
            issues = gate.history_issues(root)
            self.assertIn(("README.md", "historical personal absolute home path"), issues)
            result, output = invoke_gate(root)
            self.assertEqual(result, 1)
            self.assertFalse(private_home in output or "refs/replace/" in output, "Unsafe metadata or ref value was emitted")

    @unittest.skipUnless(shutil.which("gitleaks"), "Pinned Gitleaks is required for replacement-object secret regressions")
    def test_real_gitleaks_sees_original_blob_even_with_replacement(self):
        secret = hashlib.sha256(b"synthetic-replaced-blob-credential").hexdigest()
        with fixture_repository() as (root, git):
            base = gate.git_output(root, "rev-parse", "HEAD").decode().strip()
            (root / "README.md").write_text("api_key=" + secret)
            git("add", "README.md")
            git("commit", "-m", "Synthetic replaced tree")
            original = gate.git_output(root, "rev-parse", "HEAD").decode().strip()
            clean_tree = gate.git_output(root, "rev-parse", base + "^{tree}").decode().strip()
            clean = gate.git_output(root, "commit-tree", clean_tree, "-p", base, "-m", "Public replacement tree").decode().strip()
            git("replace", original, clean)
            git("checkout", base, "--", "README.md")
            with mock.patch.dict(os.environ, {"GIT_NO_REPLACE_OBJECTS": "0"}):
                result = subprocess.run([shutil.which("gitleaks"), "--redact=100", "--no-banner", "--config", str(root / ".gitleaks.toml"), "git", "--log-opts=--all", str(root)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False, env=gate.inspection_environment())
                self.assertEqual(result.returncode, 1, "Gitleaks did not inspect the original stored blob")
                status, output = invoke_gate(root)
            self.assertEqual(status, 1)
            self.assertFalse(secret in output or "refs/replace/" in output, "Unsafe metadata or ref value was emitted")

    @unittest.skipUnless(shutil.which("gitleaks"), "Pinned Gitleaks is required to inspect every child environment")
    def test_all_git_and_gitleaks_children_force_no_replace_objects(self):
        with fixture_repository() as (root, _):
            original_popen = subprocess.Popen
            programs = []
            def checked_popen(*args, **kwargs):
                self.assertTrue(kwargs.get("env", {}).get("GIT_NO_REPLACE_OBJECTS") == "1", "Inspection child did not force raw object access")
                programs.append(Path(args[0][0]).name)
                return original_popen(*args, **kwargs)
            with mock.patch.dict(os.environ, {"GIT_NO_REPLACE_OBJECTS": "0"}), mock.patch.object(gate.subprocess, "Popen", side_effect=checked_popen):
                result, _ = invoke_gate(root)
            self.assertEqual(result, 0)
            self.assertIn("git", programs)
            self.assertIn("gitleaks", programs)

    @unittest.skipUnless(shutil.which("gitleaks"), "Pinned Gitleaks is required for the official bot sign-off regression")
    def test_exact_dependabot_identity_and_official_trailer_pass_complete_gate(self):
        with fixture_repository() as (root, git):
            message = "Public dependency update\n\n" + BOT_SIGNOFF + "\n"
            bot_commit(git, message)
            raw = gate.git_output(root, "cat-file", "commit", "HEAD")
            records, issues = gate.metadata_records(root)
            self.assertEqual(issues, [])
            self.assertTrue(any(data == raw for _, data in records), "Original bot commit record was changed")
            self.assertFalse(BOT_SIGNOFF.encode() in gate.commit_privacy_view(raw), "Exact bot trailer was not recognized")
            result, _ = invoke_gate(root)
            self.assertEqual(result, 0)

    def test_dependabot_trailer_exception_does_not_expand_to_other_identities_or_text(self):
        personal_email = "person" + "@" + "private" + ".com"
        cases = [
            ("ordinary author", BOT_SIGNOFF, "Fixture", "123+fixture@users.noreply.github.com", ""),
            ("near bot name", BOT_SIGNOFF, "Dependabot[bot]", BOT_EMAIL, ""),
            ("near bot email", BOT_SIGNOFF, BOT_NAME, "123+dependabot[bot]@users.noreply.github.com", ""),
            ("trailer case", BOT_SIGNOFF.replace("Signed-off-by", "signed-off-by"), BOT_NAME, BOT_EMAIL, ""),
            ("trailer indentation", " " + BOT_SIGNOFF, BOT_NAME, BOT_EMAIL, ""),
            ("trailer spacing", BOT_SIGNOFF.replace(": ", ":  "), BOT_NAME, BOT_EMAIL, ""),
            ("trailer trailing space", BOT_SIGNOFF + " ", BOT_NAME, BOT_EMAIL, ""),
            ("other mailbox", BOT_SIGNOFF.replace("support", "other"), BOT_NAME, BOT_EMAIL, ""),
            ("other address", "Signed-off-by: dependabot[bot] <" + personal_email + ">", BOT_NAME, BOT_EMAIL, ""),
            ("privacy beside exact trailer", BOT_SIGNOFF, BOT_NAME, BOT_EMAIL, personal_email + "\n\n"),
            ("trailer in ordinary body", BOT_SIGNOFF + "\n\nLater prose", BOT_NAME, BOT_EMAIL, ""),
            ("duplicate official trailer", BOT_SIGNOFF + "\n" + BOT_SIGNOFF, BOT_NAME, BOT_EMAIL, ""),
        ]
        for label, trailer, name, email, extra in cases:
            with self.subTest(label=label), fixture_repository() as (root, git):
                bot_commit(git, "Public dependency update\n\n" + extra + trailer + "\n", name=name, email=email)
                result, output = invoke_gate(root)
                self.assertEqual(result, 1)
                self.assertIn("non-example personal email", output)
                self.assertFalse(any(value in output for value in [personal_email, BOT_SIGNOFF]), "Matched trailer/privacy text was emitted")

    def test_dependabot_signoff_is_not_exempt_in_source_tags_or_refs(self):
        for label in ["source", "tag", "ref"]:
            with self.subTest(label=label), fixture_repository() as (root, git):
                bot_commit(git, "Public dependency update\n")
                if label == "source":
                    (root / "README.md").write_text(BOT_SIGNOFF + "\n")
                    git("add", "README.md")
                    bot_commit(git, "Public message\n")
                elif label == "tag":
                    git("-c", "user.name=" + BOT_NAME, "-c", "user.email=" + BOT_EMAIL, "tag", "-a", "v1.0.0", "-m", BOT_SIGNOFF)
                else:
                    git("update-ref", "refs/heads/" + "support" + "@" + "github.com", "HEAD")
                result, output = invoke_gate(root)
                self.assertEqual(result, 1)
                self.assertIn("non-example personal email", output)
                self.assertFalse(BOT_SIGNOFF in output, "Matched trailer text was emitted")

    @unittest.skipUnless(shutil.which("gitleaks"), "Pinned Gitleaks is required to verify unmodified bot commit records")
    def test_bot_trailer_does_not_filter_secrets_or_raw_gitleaks_input(self):
        secret = hashlib.sha256(b"synthetic-bot-message-credential").hexdigest()
        with fixture_repository() as (root, git):
            bot_commit(git, "Public dependency update\n\napi_key=" + secret + "\n\n" + BOT_SIGNOFF + "\n")
            raw = gate.git_output(root, "cat-file", "commit", "HEAD")
            records, issues = gate.metadata_records(root)
            self.assertEqual(issues, [], "This fixture must reach Gitleaks")
            self.assertTrue(any(data == raw for _, data in records), "Original bot commit record was changed")
            original_run = subprocess.run
            inspected = []
            def checked_run(args, **kwargs):
                if args[-2] == "dir" and Path(args[-1]).name == "metadata":
                    inspected.append(True)
                    self.assertTrue(any(path.read_bytes() == raw for path in Path(args[-1]).glob("*.txt")), "Gitleaks received a filtered commit record")
                return original_run(args, **kwargs)
            with mock.patch.object(gate.subprocess, "run", side_effect=checked_run):
                result, output = invoke_gate(root)
            self.assertEqual(result, 1)
            self.assertTrue(inspected, "Metadata scanner was not reached")
            self.assertIn("Gitleaks rejected Git metadata", output)
            self.assertFalse(secret in output or BOT_SIGNOFF in output, "Matched metadata was emitted")


if __name__ == "__main__":
    unittest.main()
