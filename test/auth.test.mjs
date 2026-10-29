import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { digest } from '../build/model.js';
import { Store, tokenScope } from '../build/state.js';
import { bearer } from '../build/channels/auth.js';
import { ChannelError, ChannelHTTP } from '../build/channels/http.js';
import { reconcile, report } from '../build/engine.js';

function fixture(channel) {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-auth-'));
  const target = { channel, account: 'synthetic-client', app_id: 'com.example.app', client_id: 'synthetic-client',
    artifact: { path: path.join(root, 'fixture.apk'), filename: 'fixture.apk', sha256: 'a'.repeat(64), size: 10,
      signer_sha256: 'b'.repeat(64), package_name: 'com.example.app', version_name: '2.0', version_code: '2', abis: ['arm64-v8a'] } };
  const body = { schema_version: 2, batch_id: 'auth-batch', project_id: 'synthetic', package_name: 'com.example.app',
    version_name: '2.0', version_code: '2', signer_sha256: 'b'.repeat(64), release_notes: 'Synthetic update notes',
    release_policy: 'after_approval', targets: [target] };
  const store = new Store(root);
  store.saveBatch({ ...body, digest: digest(body), created_at: new Date().toISOString() });
  return { root, store, target };
}

function issued(channel) {
  return Response.json(channel === 'honor'
    ? { token_type: 'Bearer', expires_in: 3600, access_token: 'synthetic-token' }
    : { errno: 0, data: { access_token: 'synthetic-token', expire_in: Math.floor(Date.now() / 1000) + 3600 } });
}

for (const [channel, status, response] of [
  ['oppo', 200, { errno: 1001 }],
  ['oppo', 400, { errno: 1001 }],
  ['honor', 200, { error: 'invalid_client' }],
  ['honor', 400, { error: 'invalid_client', error_description: 'sensitive vendor text is not exposed' }],
  ['honor', 401, { error: 'invalid_client' }],
]) {
  test(`${channel} token rejection (${status}) permits a corrected request after restart`, async () => {
    const fixtureValue = fixture(channel);
    const { root, target } = fixtureValue;
    let store = fixtureValue.store;
    let calls = 0;
    const http = new ChannelHTTP(channel, async () => {
      calls++;
      assert.equal(store.accountToken(target).effect, 'intent');
      return calls === 1 ? Response.json(response, { status }) : issued(channel);
    });
    try {
      await assert.rejects(bearer(channel, target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http),
        /token_rejected.*修正后重试/);
      assert.equal(store.accountToken(target).effect, 'confirmed_rejected');
      assert.equal(store.summary('auth-batch').recovery_required, false);
      store.close();
      store = new Store(root);
      assert.equal(await bearer(channel, target.account, target.client_id, { client_secret: 'corrected-synthetic-secret' }, store, http), 'synthetic-token');
      assert.equal(await bearer(channel, target.account, target.client_id, { client_secret: 'corrected-synthetic-secret' }, store, http), 'synthetic-token');
      assert.equal(calls, 2);
      assert.doesNotMatch(JSON.stringify(store.summary('auth-batch')) + report(store, 'auth-batch'), /synthetic-token|synthetic-secret|sensitive vendor text/);
    } finally { store.close(); fs.rmSync(root, { recursive: true, force: true }); }
  });
}

for (const channel of ['honor', 'oppo']) {
  test(`${channel} lost token response remains blocked and is visible to status and reconcile`, async () => {
    const fixtureValue = fixture(channel);
    const { root, target } = fixtureValue;
    let store = fixtureValue.store;
    let calls = 0;
    const http = new ChannelHTTP(channel, async () => { calls++; throw new Error('response lost'); });
    try {
      await assert.rejects(bearer(channel, target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http), /network_or_timeout/);
      store.close();
      store = new Store(root);
      await assert.rejects(bearer(channel, target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http), /token result is unresolved/);
      const summary = store.summary('auth-batch');
      assert.equal(summary.recovery_required, true);
      assert.equal(summary.targets[0].recovery_required, true);
      assert.equal(summary.targets[0].account_token_status, 'unknown');
      assert.equal(summary.targets[0].recovery[0].kind, 'account_token');
      assert.match(summary.targets[0].recovery[0].next_action, /维护者/);
      const result = await reconcile(store, 'auth-batch', { forChannel: () => { throw new Error('must not prompt for credentials'); } });
      assert.match(result.errors[channel], /令牌申请结果未确定/);
      assert.equal(calls, 1);
      assert.equal(store.accountToken(target).effect, 'unknown');
      assert.match(report(store, 'auth-batch'), /账号令牌申请结果未确定/);
    } finally { store.close(); fs.rmSync(root, { recursive: true, force: true }); }
  });
}

for (const [channel, status, response] of [
  ['honor', 400, { error: 'unrecognized_error' }],
  ['honor', 400, { error: 'invalid_client', access_token: 'ambiguous-token' }],
  ['honor', 400, { error: 'invalid_client', access_token: '' }],
  ['honor', 200, { expires_in: 3600 }],
  ['oppo', 200, { errno: '1001' }],
  ['oppo', 200, { errno: 1001, data: { access_token: 'ambiguous-token' } }],
  ['oppo', 200, { errno: 1001, access_token: 'ambiguous-token' }],
  ['oppo', 500, { errno: 1001 }],
]) {
  test(`${channel} ambiguous token response (${status}, ${JSON.stringify(response)}) stays unresolved`, async () => {
    const { root, store, target } = fixture(channel);
    let calls = 0;
    const http = new ChannelHTTP(channel, async () => { calls++; return Response.json(response, { status }); });
    try {
      await assert.rejects(bearer(channel, target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http));
      assert.equal(store.accountToken(target).effect, 'unknown');
      await assert.rejects(bearer(channel, target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http), /token result is unresolved/);
      assert.equal(calls, 1);
    } finally { store.close(); fs.rmSync(root, { recursive: true, force: true }); }
  });
}

test('missing credentials do not leave an account intent behind', async () => {
  const { root, store, target } = fixture('honor');
  let calls = 0;
  const http = new ChannelHTTP('honor', async () => { calls++; return issued('honor'); });
  try {
    await assert.rejects(bearer('honor', target.account, target.client_id, {}, store, http), /missing_required_input/);
    assert.equal(store.accountToken(target), undefined);
    assert.equal(calls, 0);
    await bearer('honor', target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http);
    assert.equal(calls, 1);
  } finally { store.close(); fs.rmSync(root, { recursive: true, force: true }); }
});

test('an interrupted token request retains its durable intent and blocks reconcile without credentials', async () => {
  const fixtureValue = fixture('oppo');
  const { root, target } = fixtureValue;
  fixtureValue.store.beginToken(tokenScope('oppo', target.account, target.account));
  fixtureValue.store.close();
  const store = new Store(root);
  try {
    assert.equal(store.summary('auth-batch').targets[0].recovery[0].effect, 'intent');
    const result = await reconcile(store, 'auth-batch', { forChannel: () => { throw new Error('must not request credentials'); } });
    assert.ok(result.errors.oppo);
    assert.equal(store.accountToken(target).effect, 'intent');
  } finally { store.close(); fs.rmSync(root, { recursive: true, force: true }); }
});

test('a token request proven not sent permits a corrected request', async () => {
  const { root, store, target } = fixture('honor');
  try {
    await assert.rejects(bearer('honor', target.account, target.client_id, { client_secret: 'synthetic-secret' }, store,
      { send: async () => { throw new ChannelError('endpoint_not_allowed', 'not_sent'); } }));
    assert.equal(store.accountToken(target).effect, 'confirmed_not_applied');
    const http = new ChannelHTTP('honor', async () => issued('honor'));
    await bearer('honor', target.account, target.client_id, { client_secret: 'synthetic-secret' }, store, http);
    assert.equal(store.accountToken(target).effect, 'confirmed_applied');
  } finally { store.close(); fs.rmSync(root, { recursive: true, force: true }); }
});

test('a business write HTTP error cannot use the token rejection recovery path', async () => {
  const http = new ChannelHTTP('honor', async () => Response.json({ error: 'invalid_client' }, { status: 400 }), () => true);
  await assert.rejects(http.json('/openapi/v1/publish/submit-audit', 'POST', 'write', {}, 'synthetic-op'),
    error => error.code === 'http_status' && error.effect === 'unknown');
});
