import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { resolveCredentialInput, CredentialInputError } from '../build/credentials.js';

function fixture(t) {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-credential-input-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}
test('credential text preserves multiline material and all six channel field contracts', () => {
  const invalid = new CredentialInputError('synthetic-private-code');
  assert.equal(invalid.code, 'input'); assert.ok(!invalid.message.includes('synthetic-private-code'));
  const json = '{\n  "key_id": "synthetic", "sub_account": "synthetic", "private_key": "synthetic"\n}';
  const pem = '-----BEGIN PUBLIC KEY-----\nsynthetic\n-----END PUBLIC KEY-----';
  for (const [channel, input] of Object.entries({
    huawei: { service_account_json: json }, honor: { client_secret: 'synthetic' },
    xiaomi: { api_secret: 'synthetic', public_key_pem: pem, test_account_json: '{"synthetic":true}' },
    oppo: { client_secret: 'synthetic' }, vivo: { access_secret: 'synthetic' }, tencent: { api_secret: 'synthetic' },
  })) assert.deepEqual(resolveCredentialInput(channel, input), input);
});
test('private credential paths use the same owner, mode and symlink checks as CLI', t => {
  const root = fixture(t), file = path.join(root,'synthetic.json');
  fs.writeFileSync(file, '{"synthetic":true}\n', { mode:0o600 });
  assert.deepEqual(resolveCredentialInput('huawei', {}, { service_account_json: file }), { service_account_json: '{"synthetic":true}' });
  fs.chmodSync(file,0o644);
  assert.throws(() => resolveCredentialInput('huawei', {}, { service_account_json: file }), error => error instanceof CredentialInputError && error.code==='file' && !error.message.includes(file));
  fs.chmodSync(file,0o600);
  const getuid=process.getuid;
  try {
    process.getuid=()=>getuid()+1;
    assert.throws(()=>resolveCredentialInput('huawei',{}, {service_account_json:file}),error=>error instanceof CredentialInputError && error.code==='file');
  } finally {process.getuid=getuid;}
  const alias=path.join(root,'alias.json');fs.symlinkSync(file,alias);
  assert.throws(() => resolveCredentialInput('huawei', {}, { service_account_json: alias }), /0600/);
  assert.throws(() => resolveCredentialInput('huawei', {}, { service_account_json: 'relative.json' }), /绝对文件路径/);
});
test('mixed text and private-file inputs support optional Xiaomi test accounts and reject ambiguity', t => {
  const root = fixture(t), pem=path.join(root,'synthetic.pem'), json=path.join(root,'synthetic.json');
  fs.writeFileSync(pem,'-----BEGIN PUBLIC KEY-----\nsynthetic\n-----END PUBLIC KEY-----\n',{mode:0o600});
  fs.writeFileSync(json,'{"synthetic":true}',{mode:0o600});
  const resolved=resolveCredentialInput('xiaomi',{api_secret:'synthetic'},{public_key_pem:pem,test_account_json:json});
  assert.match(resolved.public_key_pem,/\nsynthetic\n/);assert.equal(resolved.test_account_json,'{"synthetic":true}');
  assert.throws(()=>resolveCredentialInput('huawei',{service_account_json:'{}'},{service_account_json:json}),/重复/);
  assert.throws(()=>resolveCredentialInput('honor',{client_secret:'synthetic'},{client_secret:json}),/不属于/);
  assert.throws(()=>resolveCredentialInput('honor',{api_secret:'synthetic'}),/不属于/);
  assert.throws(()=>resolveCredentialInput('huawei',{service_account_json:'synthetic invalid input'}),error=>!error.message.includes('synthetic invalid input'));
});
