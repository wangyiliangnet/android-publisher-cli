#!/usr/bin/env python3
"""Exercise the exact inline release writer with a durable, fault-injected API."""
import contextlib
import copy
import hashlib
import io
import pathlib
import subprocess
import tempfile
import textwrap
import types
import unittest
import urllib.parse
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / '.github/workflows/release.yml').read_text()
START = '          # BEGIN CI_DRAFT_RECOVERY\n'
END = '          # END CI_DRAFT_RECOVERY\n'
assert WORKFLOW.count(START) == WORKFLOW.count(END) == 1
CODE = textwrap.dedent(WORKFLOW.split(START, 1)[1].split(END, 1)[0])
NAMESPACE = {'__name__': 'release_draft_test'}
exec(compile(CODE, str(ROOT / '.github/workflows/release.yml'), 'exec'), NAMESPACE)
ReleaseError = NAMESPACE['ReleaseError']
release_draft = NAMESPACE['release_draft']
SHA = 'a' * 40
VERSION = '0.1.0-test.1'
REPO = 'example/android-publisher-cli'
BASE = 'repos/' + REPO
NAME = f'android-publisher-cli-{VERSION}-darwin-arm64-ci-preview.tar.gz'


class FakeAPI:
    """Writes persist before injected lost responses, just as on the server."""
    def __init__(self):
        self.main = SHA
        self.tag = None
        self.release = None
        self.assets = []
        self.contents = {}
        self.effects = []
        self.fail = None
        self.next_asset_id = 100

    def fault(self, point):
        if self.fail == point:
            self.fail = None
            raise ReleaseError('Injected unknown HTTP result')

    def __call__(self, method, endpoint, **kwargs):
        if method == 'GET':
            if endpoint == BASE + '/commits/main':
                return {'sha': self.main}
            if endpoint.startswith(BASE + '/git/ref/tags/'):
                return copy.deepcopy(self.tag)
            if endpoint.startswith(BASE + '/releases?'):
                self.fault('list-before')
                return [copy.deepcopy(self.release)] if self.release else []
            if endpoint == BASE + '/releases/1':
                return copy.deepcopy(self.release)
            if endpoint == BASE + '/releases/1/assets?per_page=100':
                return copy.deepcopy(self.assets)
            if endpoint.startswith(BASE + '/releases/assets/'):
                return self.contents[int(endpoint.rsplit('/', 1)[1])]
        if method == 'POST' and endpoint == BASE + '/git/refs':
            self.fault('tag-before')
            if self.tag:
                raise AssertionError('Existing tag was overwritten')
            payload = kwargs['payload']
            self.tag = {'ref': payload['ref'], 'object': {'type': 'commit', 'sha': payload['sha']}}
            self.effects.append('tag')
            self.fault('tag-after')
            return copy.deepcopy(self.tag)
        if method == 'POST' and endpoint == BASE + '/releases':
            self.fault('release-before')
            if self.release:
                raise AssertionError('Existing draft was recreated')
            self.release = {**copy.deepcopy(kwargs['payload']), 'id': 1, 'published_at': None}
            self.effects.append('release')
            self.fault('release-after')
            return copy.deepcopy(self.release)
        upload_prefix = f'https://uploads.github.com/repos/{REPO}/releases/1/assets?name='
        if method == 'POST' and endpoint.startswith(upload_prefix):
            name = urllib.parse.unquote(endpoint[len(upload_prefix):])
            self.fault('upload-before:' + name)
            if any(a['name'] == name for a in self.assets):
                raise AssertionError('Existing asset was clobbered')
            content = kwargs['source'].read_bytes()
            asset = {'id': self.next_asset_id, 'name': name, 'state': 'uploaded', 'size': len(content)}
            self.next_asset_id += 1
            self.assets.append(asset)
            self.contents[asset['id']] = content
            self.effects.append('upload:' + name)
            self.fault('upload-after:' + name)
            return copy.deepcopy(asset)
        raise AssertionError((method, endpoint, kwargs))


class DraftRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.workspace = tempfile.TemporaryDirectory()
        self.addCleanup(self.workspace.cleanup)
        self.root = pathlib.Path(self.workspace.name)
        data = b'synthetic test archive; no executable or production data'
        (self.root / NAME).write_bytes(data)
        (self.root / (NAME + '.sha256')).write_text(hashlib.sha256(data).hexdigest() + '  ' + NAME + '\n')
        self.api = FakeAPI()

    def run_writer(self):
        with contextlib.redirect_stdout(io.StringIO()):
            release_draft(self.api, VERSION, SHA, REPO, self.root)

    def prepared(self):
        self.run_writer()
        self.api.effects.clear()

    def assert_rejected_without_writes(self):
        before = copy.deepcopy((self.api.tag, self.api.release, self.api.assets, self.api.contents))
        with self.assertRaises(ReleaseError):
            self.run_writer()
        self.assertEqual([], self.api.effects)
        self.assertEqual(before, (self.api.tag, self.api.release, self.api.assets, self.api.contents))

    def test_initial_success_and_exact_rerun_do_not_clobber(self):
        self.run_writer()
        self.assertEqual(['tag', 'release', 'upload:' + NAME, 'upload:' + NAME + '.sha256'], self.api.effects)
        self.api.effects.clear()
        self.run_writer()
        self.assertEqual([], self.api.effects)
        self.assertEqual(2, len(self.api.assets))
        self.assertTrue(self.api.release['draft'])

    def test_tag_succeeds_release_creation_fails_then_rerun_recovers(self):
        self.api.fail = 'release-before'
        with self.assertRaises(ReleaseError):
            self.run_writer()
        self.assertIsNotNone(self.api.tag)
        self.assertIsNone(self.api.release)
        self.run_writer()
        self.assertEqual(1, self.api.effects.count('tag'))
        self.assertEqual(1, self.api.effects.count('release'))
        self.assertEqual(2, len(self.api.assets))

    def test_lost_tag_response_is_reconciled_on_rerun(self):
        self.api.fail = 'tag-after'
        with self.assertRaises(ReleaseError):
            self.run_writer()
        self.assertIsNotNone(self.api.tag)
        self.run_writer()
        self.assertEqual(1, self.api.effects.count('tag'))

    def test_lost_draft_response_is_reconciled_on_rerun(self):
        self.api.fail = 'release-after'
        with self.assertRaises(ReleaseError):
            self.run_writer()
        self.assertIsNotNone(self.api.release)
        self.run_writer()
        self.assertEqual(1, self.api.effects.count('release'))

    def test_partial_upload_and_unknown_upload_response_only_fill_missing(self):
        for point in ['upload-before:' + NAME + '.sha256', 'upload-after:' + NAME]:
            with self.subTest(point=point):
                self.api = FakeAPI()
                self.api.fail = point
                with self.assertRaises(ReleaseError):
                    self.run_writer()
                self.assertEqual([NAME], [a['name'] for a in self.api.assets])
                self.run_writer()
                self.assertEqual(1, self.api.effects.count('upload:' + NAME))
                self.assertEqual(1, self.api.effects.count('upload:' + NAME + '.sha256'))

    def test_unknown_listing_result_never_means_release_absent(self):
        self.api.tag = {'ref': 'refs/tags/v' + VERSION, 'object': {'type': 'commit', 'sha': SHA}}
        self.api.fail = 'list-before'
        self.assert_rejected_without_writes()

    def test_conflicting_or_annotated_tag_is_rejected(self):
        for obj in [{'type': 'commit', 'sha': 'b' * 40}, {'type': 'tag', 'sha': SHA}]:
            with self.subTest(obj=obj):
                self.api.tag = {'ref': 'refs/tags/v' + VERSION, 'object': obj}
                self.assert_rejected_without_writes()

    def test_published_release_is_immutable(self):
        self.prepared()
        self.api.release.update(draft=False, published_at='2030-01-01T00:00:00Z')
        self.assert_rejected_without_writes()
        # A published release remains protected even if someone deleted its tag.
        self.api.tag = None
        self.assert_rejected_without_writes()

    def test_conflicting_draft_metadata_is_rejected(self):
        for key, value in [('name', 'other'), ('body', 'other'), ('target_commitish', 'b' * 40), ('prerelease', False)]:
            with self.subTest(key=key):
                self.api = FakeAPI()
                self.prepared()
                self.api.release[key] = value
                self.assert_rejected_without_writes()

    def test_asset_bytes_are_downloaded_not_trusted_from_metadata(self):
        self.prepared()
        first = self.api.assets[0]
        self.api.contents[first['id']] = b'x' * first['size']
        first['digest'] = 'sha256:' + hashlib.sha256((self.root / NAME).read_bytes()).hexdigest()
        self.assert_rejected_without_writes()

    def test_unexpected_duplicate_incomplete_or_wrong_size_asset_is_rejected(self):
        mutations = [lambda a: a.append({**a[0], 'id': 999, 'name': 'unexpected'}),
                     lambda a: a.__setitem__(1, {**a[1], 'name': a[0]['name']}),
                     lambda a: a[0].update(state='starter'),
                     lambda a: a[0].update(size=a[0]['size'] + 1)]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                self.api = FakeAPI()
                self.prepared()
                mutation(self.api.assets)
                self.assert_rejected_without_writes()

    def test_stale_main_and_local_asset_corruption_are_rejected_before_writes(self):
        self.api.main = 'b' * 40
        self.assert_rejected_without_writes()
        self.api.main = SHA
        (self.root / NAME).write_bytes(b'corrupt')
        self.assert_rejected_without_writes()

    def test_api_transport_does_not_turn_http_failure_into_absence(self):
        for result, missing in [(types.SimpleNamespace(returncode=1, stderr=b'gh: Not Found (HTTP 404)\n', stdout=b''), True),
                                (types.SimpleNamespace(returncode=1, stderr=b'gh: Forbidden (HTTP 403)\n', stdout=b''), False)]:
            with mock.patch.object(subprocess, 'run', return_value=result):
                if missing:
                    self.assertIsNone(NAMESPACE['gh_api']('GET', 'synthetic', missing_ok=True))
                else:
                    with self.assertRaises(ReleaseError):
                        NAMESPACE['gh_api']('GET', 'synthetic', missing_ok=True)
        with mock.patch.object(subprocess, 'run', side_effect=subprocess.TimeoutExpired('gh', 90)):
            with self.assertRaises(ReleaseError):
                NAMESPACE['gh_api']('POST', 'synthetic', payload={})


if __name__ == '__main__':
    unittest.main()
