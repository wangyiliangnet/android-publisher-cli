import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { CredentialVault, MacKeyStore } from '../build/vault.js';

test('vault persists only authenticated ciphertext and detects tampering and account substitution', t => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-vault-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const keys = new Map();
  const store = { get: id => keys.has(id) ? Buffer.from(keys.get(id)) : undefined, put: (id, key) => keys.set(id, Buffer.from(key)), delete: id => keys.delete(id) };
  const vault = new CredentialVault(root, store);
  vault.save('tencent', 'example', { api_secret: 'synthetic-sensitive-material' });
  const directory = path.join(root, 'credentials');
  const file = path.join(directory, fs.readdirSync(directory)[0]);
  assert.equal(fs.statSync(file).mode & 0o777, 0o600);
  assert.ok(!fs.readFileSync(file, 'utf8').includes('synthetic-sensitive-material'));
  assert.equal(vault.get('tencent', 'example').api_secret, 'synthetic-sensitive-material');
  vault.save('tencent', 'example', { api_secret: 'synthetic-replacement' });
  assert.equal(keys.size, 1);
  const box = JSON.parse(fs.readFileSync(file)); box.content = Buffer.from('tampered').toString('base64');
  fs.writeFileSync(file, JSON.stringify(box));
  assert.throws(() => vault.get('tencent', 'example'), /无法解密/);
  vault.forget('tencent', 'example'); assert.equal(keys.size, 0); assert.equal(vault.has('tencent', 'example'), false);
});

test('keychain adapter sends bounded key material through stdin and never command arguments', () => {
  const key = Buffer.alloc(32, 0x61), calls = [];
  const run = (file, args, options) => {
    calls.push({ file, args, options });
    return { status: 0, stdout: args[0] === 'find-generic-password' ? key.toString('hex') + '\n' : '', stderr: '' };
  };
  new MacKeyStore(run).put('a'.repeat(64), key);
  assert.ok(!calls.some(call => call.args.includes(key.toString('hex'))));
  assert.equal(calls[0].args[0], '-i');
  assert.ok(calls[0].options.input.includes(key.toString('hex')));
  assert.ok(calls[0].options.input.length < 4096);
  assert.throws(() => new MacKeyStore(run).put('bad\ncommand', key), /invalid/);
});

test('isolated macOS keychain round trip', { skip: process.platform !== 'darwin' || process.env.PUBLISHER_KEYCHAIN_TESTS !== '1' }, async t => {
  const { spawnSync } = await import('node:child_process');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-keychain-'));
  const file = path.join(root, 'synthetic.keychain-db');
  const created = spawnSync('/usr/bin/security', ['create-keychain', '-p', 'synthetic-fixture-only', file], { encoding: 'utf8' });
  assert.equal(created.status, 0);
  t.after(() => { spawnSync('/usr/bin/security', ['delete-keychain', file], { stdio: 'ignore' }); fs.rmSync(root, { recursive: true, force: true }); });
  const store = new MacKeyStore(spawnSync, file), key = Buffer.alloc(32, 0x62), id = 'b'.repeat(64);
  assert.equal(store.get(id), undefined);
  store.put(id, key); assert.deepEqual(store.get(id), key);
  store.delete(id); assert.equal(store.get(id), undefined);
});

test('credential material requires the exact private-file mode', async t => {
  const { regularFile } = await import('../build/private-file.js');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-private-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const file = path.join(root, 'synthetic.json'); fs.writeFileSync(file, '{}', { mode: 0o600 });
  assert.equal(regularFile(file, 1024, true).toString(), '{}');
  fs.chmodSync(file, 0o400); assert.throws(() => regularFile(file, 1024, true), /0600/);
});

test('malformed credential JSON never appears in diagnostics', async () => {
  const { validateCredentials } = await import('../build/credentials.js');
  const input = 'synthetic-private-material-not-json';
  assert.throws(() => validateCredentials('huawei', { service_account_json: input }), error => {
    assert.match(error.message, /JSON 凭据材料格式无效/);
    assert.ok(!error.message.includes(input));
    return true;
  });
});
