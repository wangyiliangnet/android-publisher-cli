import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { digest, parseStrictJSON } from '../build/model.js';
import { Store } from '../build/state.js';
import { review } from '../build/engine.js';

const temp = () => fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-ts-'));
function batch(id, root, account = 'example-main') {
  const body = { schema_version: 2, batch_id: id, project_id: 'example', package_name: 'com.example.app',
    version_name: '2.0', version_code: '2', signer_sha256: 'a'.repeat(64), release_notes: 'Synthetic update notes',
    release_policy: 'after_approval', targets: [{ channel: 'xiaomi', account, app_id: 'com.example.app', client_id: 'example-user',
      artifact: { path: path.join(root, 'fixture.apk'), filename: 'fixture.apk', sha256: 'b'.repeat(64), size: 10,
        signer_sha256: 'a'.repeat(64), package_name: 'com.example.app', version_name: '2.0', version_code: '2', abis: ['arm64-v8a'] },
      icon_path: path.join(root, 'icon.png'), icon_sha256: 'c'.repeat(64), icon_size: 10,
      privacy_url: 'https://example.invalid/privacy' }] };
  return { ...body, digest: digest(body), created_at: new Date().toISOString() };
}
function preflight() {
  const base = { channel: 'xiaomi', account: 'example-main', app_id: 'com.example.app', package_name: 'com.example.app',
    version_code: '2', remote_version_code: '1', metadata: { app_name: 'Example' }, inspected_at: new Date().toISOString() };
  return { ...base, digest: digest(base) };
}

test('strict JSON rejects duplicate identities and preserves int64 values', () => {
  assert.throws(() => parseStrictJSON('{"batch_id":"one","batch_id":"two"}'), /duplicate/);
  assert.equal(parseStrictJSON('{"version":9007199254740993}').version, 9007199254740993n);
});

test('intent survives process restart and blocks conflicting writes', () => {
  const root = temp();
  try {
    let store = new Store(root);
    store.saveBatch(batch('batch-one', root));
    store.savePreflight('batch-one', preflight());
    const approval = review(store, 'batch-one', path.join(root, 'approval.json'));
    assert.equal(approval.approval_plan.length, 64);
    const operation = store.beginOperation('batch-one', 'xiaomi', 'push', 'd'.repeat(64));
    store.close();

    store = new Store(root);
    assert.equal(store.latestOperation('batch-one', 'xiaomi', 'push').effect, 'intent');
    assert.throws(() => store.beginOperation('batch-one', 'xiaomi', 'push', 'd'.repeat(64)), /already attempted/);
    store.saveBatch(batch('batch-two', root));
    assert.throws(() => store.savePreflight('batch-two', preflight()), /unresolved write/);
    store.finishOperation(operation, 'unknown');
    assert.equal(store.summary('batch-one').targets[0].recovery_required, true);
    const conflict = store.summary('batch-two');
    assert.equal(conflict.recovery_required, true);
    assert.equal(conflict.targets[0].recovery[0].operation_id, operation.id);
    assert.equal(conflict.targets[0].recovery[0].step, 'push');
    assert.equal(conflict.targets[0].recovery[0].batch_id, 'batch-one');
    assert.equal(conflict.targets[0].recovery[0].next_action, 'android-release batch reconcile --batch batch-one --channels xiaomi');
    assert.deepEqual(conflict.targets[0].query_support, { review: false, availability: false });
    store.close();
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});

test('verified receipt is persisted before a later command', () => {
  const root = temp();
  try {
    let store = new Store(root);
    store.saveBatch(batch('receipt-batch', root));
    store.savePreflight('receipt-batch', preflight());
    const operation = store.beginOperation('receipt-batch', 'xiaomi', 'push', 'd'.repeat(64));
    store.finishOperation(operation, 'confirmed_applied', 'ack-synthetic');
    store.close();

    store = new Store(root);
    assert.equal(store.latestOperation('receipt-batch', 'xiaomi', 'push').effect, 'confirmed_applied');
    store.setSubmitted('receipt-batch', 'xiaomi', 'ack-synthetic');
    assert.equal(store.summary('receipt-batch').accepted_targets, 1);
    store.close();
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
