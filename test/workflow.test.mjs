import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { Store } from '../build/state.js';
import { saveProject, prepare, preflight, review, submit, refresh, report } from '../build/engine.js';

const temp = () => fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-workflow-'));

function setup(root) {
  const apk = path.join(root, 'synthetic.apk');
  const notes = path.join(root, 'notes.txt');
  fs.writeFileSync(apk, 'synthetic artifact');
  fs.writeFileSync(notes, 'Synthetic update notes');
  saveProject({ schema_version: 2, project_id: 'example', package_name: 'com.example.app', signer_sha256: 'a'.repeat(64),
    channels: [{ channel: 'tencent', account: 'example-account', app_id: '456', client_id: 'example-user' }] }, root);
  const tools = { inspect: () => ({ signer_sha256: 'a'.repeat(64), package_name: 'com.example.app',
    version_name: '2.0', version_code: '2', abis: ['arm64-v8a'] }) };
  const store = new Store(root);
  const batch = prepare({ schema_version: 2, batch_id: 'example-batch', project_id: 'example', release_notes_file: notes,
    apks: { tencent: apk } }, tools, store);
  const credentials = { forChannel: async () => ({ api_secret: 'synthetic-secret' }) };
  return { tools, store, batch, credentials };
}

function network(failSubmit = false) {
  const calls = [];
  const fetcher = async (url, options) => {
    const path = new URL(url).pathname;
    calls.push(path);
    if (options.body?.[Symbol.asyncIterator]) await new Response(options.body).arrayBuffer();
    if (path === '/upload') return new Response(null, { status: 200 });
    if (path.endsWith('query_app_detail')) return Response.json({ ret: 0, pkg_name: 'com.example.app', version_code: 1,
      app_name: 'Example', introduce: 'Synthetic' });
    if (path.endsWith('get_file_upload_info')) return Response.json({ ret: 0,
      pre_sign_url: `https://example.cos.ap-shanghai.myqcloud.com/upload?q-sign-time=${Math.floor(Date.now() / 1000)}%3B${Math.floor(Date.now() / 1000) + 3600}`,
      serial_number: 'synthetic-serial' });
    if (path.endsWith('update_app')) {
      if (failSubmit) throw new Error('response was lost after the server accepted the request');
      return Response.json({ ret: 0 });
    }
    if (path.endsWith('query_app_update_status')) return Response.json({ ret: 0, audit_status: 3 });
    throw new Error(`unexpected request: ${path}`);
  };
  return { fetcher, calls };
}

test('review binds frozen APK and preflight; successful submit does not claim unbound audit', async () => {
  const root = temp();
  try {
    const { tools, store, batch, credentials } = setup(root);
    const remote = network();
    const before = await preflight(store, batch.batch_id, credentials, undefined, remote.fetcher);
    assert.deepEqual(before.errors, {});
    const approval = review(store, batch.batch_id, path.join(root, 'approval.json'));
    const result = await submit(store, batch.batch_id, path.join(root, 'approval.json'), approval.approval_plan,
      credentials, tools, false, remote.fetcher);
    assert.equal(result.accepted_targets, 1);
    assert.deepEqual(result.errors, {});
    const queried = await refresh(store, batch.batch_id, credentials, undefined, remote.fetcher);
    assert.equal(queried.approved_targets, 0);
    assert.equal(queried.live_targets, 0);
    assert.ok(!report(store, batch.batch_id).includes('synthetic-secret'));
    store.close();
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('lost submit response remains unknown and resume does not duplicate the request', async () => {
  const root = temp();
  try {
    const { tools, store, batch, credentials } = setup(root);
    const remote = network(true);
    await preflight(store, batch.batch_id, credentials, undefined, remote.fetcher);
    const approvalPath = path.join(root, 'approval.json');
    const approval = review(store, batch.batch_id, approvalPath);
    const result = await submit(store, batch.batch_id, approvalPath, approval.approval_plan,
      credentials, tools, false, remote.fetcher);
    assert.equal(result.targets[0].recovery_required, true);
    assert.equal(result.errors.tencent, 'channel protocol: network_or_timeout (unknown)');
    const writes = remote.calls.filter(path => path.endsWith('update_app')).length;
    const resumed = await submit(store, batch.batch_id, approvalPath, approval.approval_plan,
      credentials, tools, true, remote.fetcher);
    assert.equal(resumed.targets[0].recovery_required, true);
    assert.equal(remote.calls.filter(path => path.endsWith('update_app')).length, writes);
    store.close();
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
