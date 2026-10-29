import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { ensureRuntime, runtimeDiagnostic, ManagedAndroidTools } from '../build/runtime.js';
import { VERIFIER_CLASS, VERIFIER_SHA256 } from '../build/verifier-bytecode.js';

const supported = process.platform === 'darwin' && process.arch === 'arm64';
function fixture(t) {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-runtime-'));
  const previous = globalThis.fetch;
  t.after(() => { globalThis.fetch = previous; fs.rmSync(root, { recursive: true, force: true }); });
  return root;
}
test('managed diagnostic remains offline and generated verifier has its declared checksum', t => {
  const root = fixture(t);
  assert.equal(runtimeDiagnostic(root).ready, false);
  assert.deepEqual(fs.readdirSync(root), []);
  assert.equal(createHash('sha256').update(Buffer.from(VERIFIER_CLASS, 'base64')).digest('hex'), VERIFIER_SHA256);
  assert.throws(() => new ManagedAndroidTools(root).inspect('/synthetic.apk'), /doctor --repair/);
});
test('download integrity failure removes installation locks and does not activate partial components', { skip: !supported }, async t => {
  const root = fixture(t);
  globalThis.fetch = async () => new Response('synthetic corrupt download');
  await assert.rejects(ensureRuntime(root), /校验值不匹配/);
  assert.deepEqual(fs.readdirSync(path.join(root, 'runtime')), []);
  assert.equal(runtimeDiagnostic(root).ready, false);
});
test('interrupted empty lock can recover, while a live installer is not replaced', { skip: !supported }, async t => {
  const root = fixture(t), directory = path.join(root, 'runtime'); fs.mkdirSync(directory, { mode: 0o700 });
  const lock = path.join(directory, 'install.lock'); fs.writeFileSync(lock, '', { mode: 0o600 });
  globalThis.fetch = async () => new Response('synthetic corrupt download');
  await assert.rejects(ensureRuntime(root), /校验值不匹配/);
  fs.writeFileSync(lock, String(process.pid), { mode: 0o600 });
  await assert.rejects(ensureRuntime(root), /另一个进程/);
});
test('download timeouts produce a repair action and leave no partial installation', { skip: !supported }, async t => {
  const root = fixture(t);
  globalThis.fetch = async () => { throw new DOMException('synthetic timeout', 'TimeoutError'); };
  await assert.rejects(ensureRuntime(root), /doctor --repair/);
  assert.deepEqual(fs.readdirSync(path.join(root, 'runtime')), []);
});
