import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { PublisherApplication } from '../build/application.js';
import { CredentialVault } from '../build/vault.js';
import { releaseText, run } from '../build/cli.js';
import { tokenScope } from '../build/state.js';
import { generateKeyPairSync } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';

function fixture(t, { lost = false, honor = false } = {}) {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-application-'));
  const keys = new Map();
  const keyStore = { get: id => keys.has(id) ? Buffer.from(keys.get(id)) : undefined,
    put: (id, value) => keys.set(id, Buffer.from(value)), delete: id => keys.delete(id) };
  const vault = new CredentialVault(root, keyStore);
  const calls = [];
  const fetcher = async (url, init) => {
    const pathname = new URL(url).pathname;
    calls.push(pathname);
    if (init.body?.[Symbol.asyncIterator]) await new Response(init.body).arrayBuffer();
    if (pathname === '/upload') return new Response(null, { status: 200 });
    if (pathname.endsWith('query_app_detail')) return Response.json({ ret: 0, pkg_name: 'com.example.app', version_code: 1, app_name: 'Example', introduce: 'Synthetic' });
    if (pathname.endsWith('get_file_upload_info')) return Response.json({ ret: 0, pre_sign_url: `https://example.cos.ap-shanghai.myqcloud.com/upload?q-sign-time=${Math.floor(Date.now()/1000)}%3B${Math.floor(Date.now()/1000)+3600}`, serial_number: 'synthetic' });
    if (pathname.endsWith('update_app')) { if (lost) throw new Error('synthetic response loss'); return Response.json({ ret: 0 }); }
    if (pathname.endsWith('query_app_update_status')) return Response.json({ ret: 0, audit_status: 3 });
    if (pathname.endsWith('/devupload/dev/query')) return Response.json({ result: 0, packageInfo: { versionCode: 1, packageName: 'com.example.app', appName: 'Synthetic' }, updateVersion: true });
    if (pathname === '/auth/token') return Response.json({ token_type: 'Bearer', expires_in: 3600, access_token: 'synthetic-token' });
    if (pathname.endsWith('get-app-id')) return Response.json({ code: 0, data: [{ appId: 123, packageName: 'com.example.app' }] });
    if (pathname.endsWith('get-app-detail')) return Response.json({ code: 0, data: { basicInfo: { appId: 123, packageName: 'com.example.app' }, releaseInfo: { versionCode: 1 }, languageInfo: [{ languageId: 'zh-CN', appName: 'Example', intro: 'Synthetic', briefIntro: 'Brief', newFeature: 'Older' }] } });
    if (pathname.endsWith('get-file-upload-url')) {
      const [{ fileName }] = JSON.parse(String(init.body));
      return Response.json({ code: 0, data: [{ fileName, objectId: 789, expireTime: Math.floor(Date.now()/1000)+3600, uploadUrl: 'https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=789' }] });
    }
    if (pathname.endsWith('submit-audit')) return Response.json({ code: 0, data: 'synthetic-release' });
    if (pathname.endsWith('get-audit-result')) return Response.json({ code: 0, data: [{ appId: 123, releaseId: 'synthetic-release', auditResult: 1 }] });
    if (pathname.includes('/openapi/')) return Response.json({ code: 0 });
    throw new Error('unexpected synthetic request');
  };
  const seenCredentials = [];
  const credentials = { forChannel: async ch => { seenCredentials.push(ch); return ch === 'tencent' ? { api_secret: 'synthetic' } : { client_secret: 'synthetic' }; } };
  const tools = { inspect: () => ({ package_name: 'com.example.app', version_name: '2.0', version_code: '2', signer_sha256: 'a'.repeat(64), abis: ['arm64-v8a'] }) };
  const app = new PublisherApplication({ root, vault, fetcher, credentials, tools });
  app.saveApp({ schema_version: 2, project_id: 'demo', package_name: 'com.example.app', channels: [
    { channel: 'tencent', account: 'tencent-main', app_id: '456', client_id: 'example-user' },
    ...(honor ? [{ channel: 'honor', account: 'honor-main', app_id: '123', client_id: 'example-client' }] : []),
  ] });
  const apks = {};
  for (const channel of honor ? ['tencent', 'honor'] : ['tencent']) {
    const file = path.join(root, channel === 'honor' ? 'first.apk' : 'second.apk');
    fs.writeFileSync(file, `synthetic ${channel} artifact`); apks[channel] = file;
  }
  const input = { app_id: 'demo', apks, release_notes: 'Synthetic release notes' };
  t.after(() => { app.close(); fs.rmSync(root, { recursive: true, force: true }); });
  return { root, app, calls, apks, input, vault, seenCredentials };
}

test('applications and publishing accounts can be created independently and survive removing the final channel', async t => {
  const f = fixture(t);
  const profile = { channel: 'honor', account: 'shared-honor', client_id: 'shared-client' };
  assert.deepEqual(await f.app.invoke('accounts.save', { account: profile }), { account: profile });
  await f.app.updateAccountFromInput('honor', profile.account, { client_secret: 'synthetic-independent-secret' });
  assert.deepEqual(f.app.accounts().find(row => row.account === profile.account).apps, []);
  assert.equal(f.vault.has('honor', profile.account), true);
  assert.deepEqual(f.calls, []);
  for (const id of ['first', 'second']) {
    f.app.saveApp({ schema_version: 2, project_id: id, display_name: id, package_name: 'com.example.app', channels: [] });
    assert.deepEqual(f.app.appOverviews().find(row => row.app_id === id).channels, []);
  }
  await assert.rejects(f.app.prepareRelease({ app_id: 'first', apks: {}, release_notes: 'Synthetic notes' }), /尚未配置发布渠道/);
  for (const id of ['first', 'second']) f.app.saveApp({ ...f.app.app(id), channels: [{ ...profile, app_id: '123' }] }, true);
  assert.equal(f.app.appOverviews().find(row => row.app_id === 'first').channels[0].connected, false);
  await f.app.checkApp('first');
  assert.equal(f.app.appOverviews().find(row => row.app_id === 'first').channels[0].connected, true);
  assert.equal(f.app.appOverviews().find(row => row.app_id === 'second').channels[0].connected, false);
  assert.throws(() => f.app.saveAccount({ ...profile, account: 'alias' }), /选择已有渠道账号/);
  assert.throws(() => f.app.saveAccount({ ...profile, client_id: 'different-client' }), /API 标识保持不变/);
  assert.throws(() => f.app.saveAccount({ ...profile, credentials: 'synthetic' }), /unexpected/);
  for (const id of ['first', 'second']) f.app.saveApp({ ...f.app.app(id), channels: [] }, true);
  const restarted = new PublisherApplication({ root: f.root, vault: f.vault });
  try {
    assert.deepEqual(restarted.app('first').channels, []);
    assert.deepEqual(restarted.accounts().find(row => row.account === profile.account).apps, []);
    assert.equal(restarted.accounts().find(row => row.account === profile.account).saved, true);
    await restarted.updateAccountFromInput('honor', profile.account, { client_secret: 'synthetic-replacement' });
    assert.equal(f.vault.get('honor', profile.account).client_secret, 'synthetic-replacement');
  } finally { restarted.close(); }
});

test('shared account credential updates record access separately when one application rejects access', async t => {
  const f = fixture(t);
  f.app.saveApp({ schema_version: 2, project_id: 'other', package_name: 'com.example.denied', channels: [
    { channel: 'tencent', account: 'tencent-main', app_id: '789', client_id: 'example-user' },
  ] });
  await f.app.updateAccount('tencent', 'tencent-main', { api_secret: 'synthetic-new-secret' });
  assert.equal(f.vault.get('tencent', 'tencent-main').api_secret, 'synthetic-new-secret');
  const overviews = f.app.appOverviews();
  assert.equal(overviews.find(row => row.app_id === 'demo').channels[0].connected, true);
  assert.equal(overviews.find(row => row.app_id === 'other').channels[0].connected, false);
  assert.match(overviews.find(row => row.app_id === 'other').channels[0].issue, /账号访问检查未通过/);
});

test('CLI creates an application without channels and reuses an independently created account', async t => {
  const f = fixture(t), output = [], messages = [];
  const terminal = answers => ({ ask: async label => { assert.ok(answers.length, label); return answers.shift(); },
    write: value => messages.push(value), close() {}, suspend() {}, multiline: async () => '' });
  assert.equal(await run(['app', 'add', 'reader'], { application: f.app, terminal: terminal(['阅读助手', 'com.example.app', 'yes']), output: value => output.push(value) }), 0);
  assert.deepEqual(f.app.app('reader').channels, []);
  assert.deepEqual(f.calls, []);
  assert.equal(await run(['account', 'add', 'honor', 'shared-honor'], { application: f.app, terminal: terminal(['shared-client']),
    credentials: { forChannel: async () => ({ client_secret: 'synthetic' }) }, output: value => output.push(value) }), 0);
  assert.match(output.join(''), /尚未验证应用访问/);
  assert.deepEqual(f.calls, []);
  assert.equal(await run(['app', 'channels', 'reader'], { application: f.app,
    terminal: terminal(['honor', 'shared-honor', '123', 'yes']), output: value => output.push(value) }), 0);
  assert.deepEqual(f.app.app('reader').channels, [{ channel: 'honor', account: 'shared-honor', app_id: '123', client_id: 'shared-client' }]);
  assert.equal(await run(['app', 'channels', 'reader'], { application: f.app,
    terminal: terminal(['none', 'yes']), output: value => output.push(value) }), 0);
  assert.deepEqual(f.app.app('reader').channels, []);
  assert.equal(f.vault.has('honor', 'shared-honor'), true);
});

test('application setup and explicit subset preparation are offline and freeze only selected packages', async t => {
  const f = fixture(t, { honor: true });
  assert.deepEqual(f.calls, []);
  await assert.rejects(f.app.prepareRelease({ ...f.input, apks: { tencent: f.apks.tencent } }), /honor/);
  const { release } = await f.app.prepareRelease({ ...f.input, apks: { tencent: f.apks.tencent }, channels: ['tencent'] });
  assert.equal(release.targets.length, 1);
  assert.equal(release.targets[0].artifact.filename, 'second.apk');
  assert.equal(fs.statSync(release.targets[0].artifact.path).mode & 0o777, 0o600);
  assert.deepEqual(f.calls, []);
  await assert.rejects(f.app.prepareRelease({ ...f.input, channels: ['unknown'] }), /渠道/);
});

test('Xiaomi email developer accounts remain reusable after removing the channel', t => {
  const f = fixture(t);
  const project = f.app.app('demo');
  const xiaomi = { channel: 'xiaomi', account: 'xiaomi-main', app_id: 'com.example.app',
    client_id: 'developer+release@example.com', icon_path: '/private/synthetic/icon.png', privacy_url: 'https://example.com/privacy' };
  f.app.saveApp({ ...project, channels: [...project.channels, xiaomi] }, true);
  assert.equal(f.app.accounts().find(row => row.channel === 'xiaomi').client_id, xiaomi.client_id);
  f.app.saveApp(project, true);
  assert.deepEqual(f.app.accounts().find(row => row.channel === 'xiaomi'), {
    channel: 'xiaomi', account: 'xiaomi-main', client_id: xiaomi.client_id, apps: [], app_names: [], saved: false, removal: { allowed: true },
  });
  for (const client_id of ['', ' developer@example.com', 'developer\n@example.com', 'a'.repeat(257)])
    assert.throws(() => f.app.saveApp({ ...project, channels: [ { ...xiaomi, client_id } ] }, true), /client_id/);
  assert.throws(() => f.app.saveApp({ ...project, channels: [{ ...project.channels[0], client_id: 'developer@example.com' }] }, true), /client_id/);
});

test('exact materials reuse a release; changed notes create visible related content', async t => {
  const f = fixture(t);
  const a = await f.app.prepareRelease(f.input);
  const b = await f.app.prepareRelease(f.input);
  assert.equal(b.duplicate, true); assert.equal(b.release.batch_id, a.release.batch_id);
  const c = await f.app.prepareRelease({ ...f.input, release_notes: 'Changed synthetic release notes' });
  assert.equal(c.duplicate, false); assert.equal(c.related_release, a.release.batch_id);
  assert.equal(f.app.releases('demo').length, 2);
  assert.equal(f.app.release(a.release.batch_id).release_notes, f.input.release_notes);
});

test('readable application names keep stable identities and connection readiness follows the saved binding', async t => {
  const f = fixture(t);
  const project = { ...f.app.app('demo'), display_name: '示例阅读' };
  f.app.saveApp(project, true);
  assert.equal(f.app.apps()[0].display_name, '示例阅读');
  assert.equal(f.app.appOverviews()[0].channels[0].saved, false);
  await f.app.updateAccount('tencent', 'tencent-main', { api_secret: 'synthetic' });
  let channel = f.app.appOverviews()[0].channels[0];
  assert.equal(channel.saved, true); assert.equal(channel.connected, true); assert.ok(channel.checked_at);
  f.app.saveApp({ ...project, channels: [{ ...project.channels[0], app_id: '789' }] }, true);
  channel = f.app.appOverviews()[0].channels[0];
  assert.equal(channel.saved, true); assert.equal(channel.connected, false);
  await f.app.checkApp('demo', ['tencent']);
  assert.equal(f.app.appOverviews()[0].channels[0].connected, true);
  const { release } = await f.app.prepareRelease(f.input);
  assert.equal(release.display_name, '示例阅读'); assert.equal(release.project_id, 'demo');
  assert.equal(release.can_edit, true); assert.equal(release.has_writes, false);
  f.app.forgetAccount('tencent', 'tencent-main');
  assert.equal(f.app.appOverviews()[0].channels[0].connected, false);
  assert.throws(() => f.app.saveApp({ ...project, display_name: 'line\nbreak' }, true));
});

test('explicit retry confirmation retains the whole release and cannot repurpose an old narrow plan', async t => {
  const f = fixture(t, { honor: true });
  const { release } = await f.app.prepareRelease(f.input);
  await f.app.preflightRelease(release.batch_id);
  const oldPlan = f.app.reviewRelease(release.batch_id, ['honor']).plan;
  const unknown = f.app.store.beginOperation(release.batch_id, 'tencent', 'authorize', 'a'.repeat(64));
  f.app.store.finishOperation(unknown, 'unknown');
  const notSent = f.app.store.beginOperation(release.batch_id, 'honor', 'authorize', 'b'.repeat(64));
  f.app.store.finishOperation(notSent, 'confirmed_not_applied');
  assert.equal(f.app.release(release.batch_id).can_edit, false);
  await assert.rejects(f.app.submitRelease(release.batch_id, oldPlan.approval_plan, true));
  assert.throws(() => f.app.reviewRelease(release.batch_id, ['honor']), /须保留/);
  assert.throws(() => f.app.reviewRelease(release.batch_id, ['tencent'], true), /待核对/);
  const { plan } = f.app.reviewRelease(release.batch_id, ['honor'], true);
  const result = await f.app.submitRelease(release.batch_id, plan.approval_plan, true);
  assert.equal(result.release.targets.length, 2);
  assert.deepEqual(result.release.selected_channels, ['tencent', 'honor']);
  assert.equal(result.release.accepted_targets, 1); assert.equal(result.release.all_accepted, false);
  assert.equal(result.release.recovery_required, true);
  assert.equal(f.app.store.operations(release.batch_id, 'tencent').length, 1);
  assert.ok(!f.calls.some(url => url.endsWith('get_file_upload_info')));
});

test('CLI continues safe channels while explaining unresolved submissions in the same release', async t => {
  const f = fixture(t, { honor: true });
  const { release } = await f.app.prepareRelease(f.input);
  assert.doesNotMatch(releaseText(release), /需到商店查看/);
  await f.app.preflightRelease(release.batch_id);
  const unknown = f.app.store.beginOperation(release.batch_id, 'tencent', 'authorize', 'a'.repeat(64));
  f.app.store.finishOperation(unknown, 'unknown');
  const stopped = f.app.store.beginOperation(release.batch_id, 'honor', 'authorize', 'b'.repeat(64));
  f.app.store.finishOperation(stopped, 'confirmed_not_applied');
  const answers = ['continue', 'yes'], messages = [];
  const io = { ask: async () => answers.shift(), write: text => messages.push(text), multiline: async () => '', suspend() {}, close() {} };
  const code = await run(['publish', 'demo'], { application: f.app, terminal: io, output: text => messages.push(text) });
  assert.equal(code, 3); assert.equal(answers.length, 0);
  assert.match(messages.join(''), /本次只处理所选未完成渠道/);
  assert.match(messages.join(''), /提交结果待确认/);
  const current = f.app.release(release.batch_id);
  assert.equal(current.targets.length, 2); assert.equal(current.accepted_targets, 1); assert.equal(current.recovery_required, true);
});

test('changing future account settings keeps the original release account repairable', async t => {
  const f = fixture(t, { honor: true });
  const { release } = await f.app.prepareRelease(f.input);
  await f.app.preflightRelease(release.batch_id);
  const operation = f.app.store.beginOperation(release.batch_id, 'tencent', 'authorize', 'a'.repeat(64));
  f.app.store.finishOperation(operation, 'unknown');
  const app = f.app.app('demo');
  f.app.saveApp({ ...app, channels: app.channels.map(row => row.channel === 'tencent' ? { ...row, account: 'new-account', client_id: 'new-client' } : row) }, true);
  assert.ok(f.app.accounts().some(row => row.account === 'tencent-main'));
  await f.app.updateAccountFromInput('tencent', 'tencent-main', { api_secret: 'replacement' });
  assert.equal(f.vault.get('tencent', 'tencent-main').api_secret, 'replacement');
  assert.equal(f.app.appOverviews()[0].channels.find(row => row.channel === 'tencent').saved, false);
  assert.equal(f.app.release(release.batch_id).recovery_required, true);
  f.app.forgetAccount('tencent', 'tencent-main');
  assert.equal(f.app.store.operations(release.batch_id, 'tencent').length, 1);
});

test('removing an unused channel retains its saved account for reuse or explicit clearing', async t => {
  const f = fixture(t, { honor: true });
  await f.app.updateAccount('tencent', 'tencent-main', { api_secret: 'synthetic' });
  const app = f.app.app('demo'); f.app.saveApp({ ...app, channels: app.channels.filter(row => row.channel !== 'tencent') }, true);
  const account = f.app.accounts().find(row => row.channel === 'tencent');
  assert.deepEqual(account.apps, []); assert.equal(account.saved, true); assert.equal(account.client_id, 'example-user');
  f.app.forgetAccount('tencent', 'tencent-main');
  assert.equal(f.app.accounts().find(row => row.channel === 'tencent').saved, false);
});

test('Xiaomi account onboarding supplies icon integrity evidence without an APK or business request', async t => {
  const f = fixture(t);
  const icon = path.join(f.root, 'icon.png'); fs.writeFileSync(icon, 'synthetic icon');
  f.app.saveApp({ schema_version: 2, project_id: 'reader', package_name: 'com.example.app', channels: [
    { channel: 'xiaomi', account: 'xiaomi-main', app_id: 'com.example.app', client_id: 'synthetic-user', icon_path: icon, privacy_url: 'https://example.invalid/privacy' },
  ] });
  const { publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  await f.app.updateAccount('xiaomi', 'xiaomi-main', { api_secret: 'synthetic', public_key_pem: publicKey.export({ type: 'spki', format: 'pem' }) });
  assert.equal(f.app.appOverviews().find(row => row.app_id === 'reader').channels[0].connected, true);
  assert.deepEqual(f.calls, ['/devupload/dev/query']); assert.equal(f.app.store.batchIds().length, 0);
});

test('clearing an unlinked account clears its cached token without deleting the lease record', async t => {
  const f = fixture(t, { honor: true });
  await f.app.updateAccount('honor', 'honor-main', { client_secret: 'synthetic' });
  const scope = tokenScope('honor', 'honor-main', 'example-client');
  assert.equal(f.app.store.beginToken(scope), 'synthetic-token');
  const app = f.app.app('demo'); f.app.saveApp({ ...app, channels: app.channels.filter(row => row.channel !== 'honor') }, true);
  f.app.forgetAccount('honor', 'honor-main');
  assert.equal(f.app.store.beginToken(scope), null);
  f.app.store.failToken(scope, 'confirmed_not_applied');
});

test('renaming an archived API account cannot bypass an unresolved authorization', async t => {
  const f = fixture(t, { honor: true });
  await f.app.updateAccount('honor', 'honor-main', { client_secret: 'synthetic' });
  const scope = tokenScope('honor', 'honor-main', 'example-client');
  f.app.store.invalidateAccountToken('honor', 'honor-main', 'example-client');
  f.app.store.beginToken(scope); f.app.store.failToken(scope, 'unknown');
  const app = f.app.app('demo'); f.app.saveApp({ ...app, channels: app.channels.filter(row => row.channel !== 'honor') }, true);
  f.app.forgetAccount('honor', 'honor-main');
  assert.throws(() => f.app.saveApp({ schema_version: 2, project_id: 'another', package_name: 'com.example.app',
    channels: [{ channel: 'honor', account: 'renamed', app_id: '123', client_id: 'example-client' }] }), /选择已有渠道账号/);
  assert.throws(() => f.app.store.beginToken(scope), /unresolved/);
});

test('review never submits; bound confirmation submits once and repeat skips credentials and writes', async t => {
  const f = fixture(t);
  const { release } = await f.app.prepareRelease(f.input);
  await f.app.preflightRelease(release.batch_id);
  const { plan } = f.app.reviewRelease(release.batch_id);
  assert.ok(!f.calls.some(p => p.endsWith('update_app')));
  await assert.rejects(f.app.submitRelease(release.batch_id, 'b'.repeat(64)));
  const result = await f.app.submitRelease(release.batch_id, plan.approval_plan);
  assert.deepEqual(result.errors, {}); assert.equal(result.release.all_accepted, true);
  const count = f.calls.length, credentials = f.seenCredentials.length;
  await f.app.submitRelease(release.batch_id, plan.approval_plan);
  assert.equal(f.calls.length, count); assert.equal(f.seenCredentials.length, credentials);
  const status = await f.app.getRelease(release.batch_id, true);
  assert.equal(status.release.approved_targets, 0); // Unversioned vendor audit is not proof.
});

test('lost response continues other channels; manual evidence never clears an unknown write', async t => {
  const f = fixture(t, { lost: true, honor: true });
  const { release } = await f.app.prepareRelease(f.input);
  assert.deepEqual((await f.app.preflightRelease(release.batch_id)).errors, {});
  const { plan } = f.app.reviewRelease(release.batch_id);
  const result = await f.app.submitRelease(release.batch_id, plan.approval_plan);
  assert.equal(result.release.accepted_targets, 1);
  assert.equal(result.release.recovery_required, true);
  const count = f.calls.filter(p => p.endsWith('update_app')).length;
  await f.app.submitRelease(release.batch_id, plan.approval_plan);
  assert.equal(f.calls.filter(p => p.endsWith('update_app')).length, count);
  f.app.recordManual(release.batch_id, { channel: 'tencent', review_state: 'approved', availability_state: 'live', source: 'Synthetic console', observed_at: new Date().toISOString() });
  const manual = f.app.release(release.batch_id);
  assert.equal(manual.recovery_required, true); assert.equal(manual.live_targets, 0);
  assert.equal(manual.manual_observations.length, 1);
  assert.throws(() => f.app.reviewRelease(release.batch_id), /待核对/);
  const changed = await f.app.prepareRelease({ ...f.input, release_notes: 'Another synthetic release' });
  const blocked = await f.app.preflightRelease(changed.release.batch_id);
  assert.ok(blocked.errors.tencent);
});

test('old narrow plans cannot remove executed channels, and scope-sensitive dedupe never silently narrows', async t => {
  const f = fixture(t, { honor: true });
  const { release } = await f.app.prepareRelease(f.input);
  await f.app.preflightRelease(release.batch_id);
  const tencent = f.app.reviewRelease(release.batch_id, ['tencent']).plan;
  const honor = f.app.reviewRelease(release.batch_id, ['honor']).plan;
  const submitted = await f.app.submitRelease(release.batch_id, tencent.approval_plan);
  assert.equal(submitted.release.targets.length, 1); assert.equal(submitted.release.all_accepted, true);
  await assert.rejects(f.app.submitRelease(release.batch_id, honor.approval_plan), /已有执行记录/);
  assert.deepEqual(f.app.release(release.batch_id).selected_channels, ['tencent']);
  const again = await f.app.prepareRelease(f.input);
  assert.equal(again.duplicate, false);
  assert.equal(again.release.targets.length, 2); assert.equal(again.release.all_accepted, false);
});

test('CLI executes a fully specified release with one human authorization and reads history as JSON', async t => {
  const f = fixture(t);
  const notes = path.join(f.root, 'notes.txt'); fs.writeFileSync(notes, f.input.release_notes);
  const questions = [], output = [];
  const io = { ask: async label => { questions.push(label); return 'yes'; }, write: () => {}, multiline: () => { throw new Error('unexpected notes prompt'); }, suspend: () => {}, close: () => {} };
  const code = await run(['publish', 'demo', '--apk', `tencent=${f.apks.tencent}`, '--notes-file', notes], { application: f.app, terminal: io, output: text => output.push(text) });
  assert.equal(code, 0); assert.equal(questions.length, 1); assert.match(questions[0], /上传并提交/);
  assert.match(output.join(''), /提交已完成/);
  output.length = 0;
  assert.equal(await run(['history', 'demo', '--json'], { application: f.app, terminal: io, output: text => output.push(text) }), 0);
  assert.equal(JSON.parse(output.join('')).releases[0].all_accepted, true);
});

test('cancel at summary performs no business requests and a modified frozen APK blocks submission', async t => {
  const f = fixture(t);
  const notes = path.join(f.root, 'notes.txt'); fs.writeFileSync(notes, f.input.release_notes);
  const io = { ask: async () => 'no', write: () => {}, multiline: async () => '', suspend: () => {}, close: () => {} };
  await assert.rejects(run(['publish', 'demo', '--apk', `tencent=${f.apks.tencent}`, '--notes-file', notes], { application: f.app, terminal: io, output: () => {} }), /取消/);
  assert.ok(!f.calls.some(p => p.endsWith('update_app')));
  const [release] = f.app.releases('demo');
  const { plan } = f.app.reviewRelease(release.batch_id);
  fs.appendFileSync(release.targets[0].artifact.path, 'modified');
  await assert.rejects(f.app.submitRelease(release.batch_id, plan.approval_plan), /differs/);
  assert.ok(!f.calls.some(p => p.endsWith('get_file_upload_info')));
});

test('account update proves new credentials rather than reusing an old token; forgetting retains unresolved leases', async t => {
  const f = fixture(t, { honor: true });
  const scope = tokenScope('honor', 'honor-main', 'example-client');
  f.app.store.beginToken(scope); f.app.store.finishToken(scope, 'synthetic-old-token', new Date(Date.now()+3600000).toISOString());
  await f.app.updateAccount('honor', 'honor-main', { client_secret: 'synthetic-new' });
  assert.ok(f.calls.includes('/auth/token'));
  assert.equal(f.vault.get('honor', 'honor-main').client_secret, 'synthetic-new');
  f.app.store.invalidateAccountToken('honor', 'honor-main', 'example-client');
  f.app.store.beginToken(scope); f.app.store.failToken(scope, 'unknown');
  f.app.forgetAccount('honor', 'honor-main');
  assert.equal(f.vault.has('honor', 'honor-main'), false);
  assert.throws(() => f.app.store.beginToken(scope), /unresolved|未决|unknown/);
});

test('forgetting an account removes only its own upload materials and preserves operation receipts', async t => {
  const f = fixture(t);
  f.app.saveApp({ schema_version: 2, project_id: 'other', package_name: 'com.example.app', channels: [{ channel: 'tencent', account: 'other-account', app_id: '987', client_id: 'other-user' }] });
  const a = (await f.app.prepareRelease(f.input)).release;
  const b = (await f.app.prepareRelease({ ...f.input, app_id: 'other' })).release;
  const operations = [];
  for (const release of [a,b]) {
    await f.app.preflightRelease(release.batch_id);
    const operation = f.app.store.beginOperation(release.batch_id, 'tencent', 'authorize', 'a'.repeat(64));
    f.app.store.finishOperation(operation, 'confirmed_applied', 'synthetic-receipt', { value: { synthetic: true }, expiresAt: new Date(Date.now()+3600000).toISOString() });
    operations.push(operation);
  }
  f.app.forgetAccount('tencent','tencent-main');
  assert.throws(() => f.app.store.readMaterial(operations[0]));
  assert.deepEqual(f.app.store.readMaterial(operations[1]), { synthetic: true });
  assert.equal(f.app.store.operations(a.batch_id).length, 1);
  assert.throws(() => f.app.saveApp({ schema_version: 2, project_id: 'too-long', package_name: 'com.example.app', channels: [{ channel:'xiaomi',account:'synthetic',app_id:'com.example.app',client_id:'other',icon_path:'/synthetic/icon.png',privacy_url:'https://example.invalid/'+ 'a'.repeat(4100) }] }), /privacy_url/);
});

test('removing an unused account clears credentials and tokens, survives restart, and permits explicit re-adding', async t => {
  const f = fixture(t);
  const profile = { channel: 'honor', account: 'honor-unused', client_id: 'unused-client' };
  f.app.saveAccount(profile);
  await f.app.updateAccount('honor', profile.account, { client_secret: 'synthetic-unused' });
  const scope = tokenScope('honor', profile.account, profile.client_id);
  f.app.store.beginToken(scope);
  f.app.store.finishToken(scope, 'synthetic-token', new Date(Date.now() + 3600000).toISOString());
  assert.deepEqual(f.app.accounts().find(row => row.account === profile.account).removal, { allowed: true });
  assert.deepEqual(await f.app.invoke('accounts.remove', profile), { removed: true });
  assert.equal(f.vault.has('honor', profile.account), false);
  assert.equal(f.app.store.db.prepare('SELECT token FROM token_leases WHERE scope=?').get(scope).token, '');
  assert.equal(f.app.accounts().some(row => row.account === profile.account), false);
  assert.deepEqual(f.calls, []);
  const restarted = new PublisherApplication({ root: f.root, vault: f.vault });
  try {
    assert.equal(restarted.accounts().some(row => row.account === profile.account), false);
    await assert.rejects(restarted.updateAccount('honor', profile.account, { client_secret: 'synthetic' }), /账号不存在/);
    assert.throws(() => restarted.saveAccount({ ...profile, account: 'new-alias' }), /沿用原账号备注重新添加/);
    restarted.saveAccount(profile);
    assert.equal(restarted.accounts().find(row => row.account === profile.account).saved, false);
  } finally { restarted.close(); }
});

test('account removal rejects current application bindings without clearing credentials', t => {
  const f = fixture(t);
  f.vault.save('tencent', 'tencent-main', { api_secret: 'synthetic-retained' });
  f.app.saveApp({ ...f.app.app('demo'), project_id: 'second', display_name: '第二个应用' });
  const status = f.app.accounts()[0].removal;
  assert.equal(status.allowed, false); assert.equal(status.reason, 'applications');
  assert.deepEqual(status.applications.map(app => app.id), ['demo', 'second']);
  assert.throws(() => f.app.removeAccount('tencent', 'tencent-main'), /仍被应用选用/);
  assert.equal(f.vault.has('tencent', 'tencent-main'), true);
  assert.equal(f.app.store.accountRemoved('tencent', 'tencent-main'), false);
});

test('completed historical releases retain their identities and receipts without resurrecting a removed account', async t => {
  const f = fixture(t);
  await f.app.updateAccount('tencent', 'tencent-main', { api_secret: 'synthetic' });
  const { release } = await f.app.prepareRelease(f.input);
  await f.app.preflightRelease(release.batch_id);
  const { plan } = f.app.reviewRelease(release.batch_id);
  await f.app.submitRelease(release.batch_id, plan.approval_plan);
  f.app.saveApp({ ...f.app.app('demo'), channels: [] }, true);
  const batch = f.app.store.loadBatch(release.batch_id), operations = f.app.store.operations(release.batch_id);
  assert.deepEqual(f.app.accounts()[0].removal, { allowed: true });
  f.app.removeAccount('tencent', 'tencent-main');
  assert.deepEqual(f.app.accounts(), []);
  assert.deepEqual(f.app.store.loadBatch(release.batch_id), batch);
  assert.deepEqual(f.app.store.operations(release.batch_id), operations);
  assert.equal(f.app.release(release.batch_id).targets[0].account, 'tencent-main');
  assert.equal(f.app.release(release.batch_id).all_accepted, true);
  const restarted = new PublisherApplication({ root: f.root, vault: f.vault });
  try { assert.deepEqual(restarted.accounts(), []); assert.equal(restarted.release(release.batch_id).targets[0].account, 'tencent-main'); }
  finally { restarted.close(); }
});

test('account removal keeps unstarted release materials but blocks unresolved business and token requests', async t => {
  const f = fixture(t);
  const { release } = await f.app.prepareRelease(f.input);
  f.app.saveApp({ ...f.app.app('demo'), channels: [] }, true);
  f.app.removeAccount('tencent', 'tencent-main');
  assert.equal(f.app.release(release.batch_id).has_writes, false);
  f.app.saveAccount({ channel: 'tencent', account: 'tencent-main', client_id: 'example-user' });
  await f.app.preflightRelease(release.batch_id);
  const operation = f.app.store.beginOperation(release.batch_id, 'tencent', 'authorize', 'a'.repeat(64));
  assert.throws(() => f.app.removeAccount('tencent', 'tencent-main'), /正在执行的提交或未决请求/);
  f.app.store.finishOperation(operation, 'unknown');
  assert.throws(() => f.app.removeAccount('tencent', 'tencent-main'), /正在执行的提交或未决请求/);
  f.app.store.resolveOperation(operation, 'confirmed_not_applied');
  f.app.removeAccount('tencent', 'tencent-main');
  assert.equal(f.app.store.operationById(operation.id).effect, 'confirmed_not_applied');
  f.app.saveAccount({ channel: 'honor', account: 'honor-unused', client_id: 'unused-client' });
  f.vault.save('honor', 'honor-unused', { client_secret: 'synthetic-retained' });
  const scope = tokenScope('honor', 'honor-unused', 'unused-client');
  f.app.store.beginToken(scope);
  assert.throws(() => f.app.removeAccount('honor', 'honor-unused'), /正在执行的提交或未决请求/);
  f.app.store.failToken(scope, 'unknown');
  assert.throws(() => f.app.removeAccount('honor', 'honor-unused'), /正在执行的提交或未决请求/);
  assert.equal(f.vault.has('honor', 'honor-unused'), true);
  assert.equal(f.app.store.db.prepare('SELECT effect FROM token_leases WHERE scope=?').get(scope).effect, 'unknown');
});

test('a submission awaiting local tools prevents removal before its first business intent', async t => {
  const f = fixture(t, { honor: true });
  const { release } = await f.app.prepareRelease(f.input);
  await f.app.preflightRelease(release.batch_id);
  const { plan } = f.app.reviewRelease(release.batch_id, ['tencent']);
  f.app.saveApp({ ...f.app.app('demo'), channels: [] }, true);
  const gate = Promise.withResolvers(), started = Promise.withResolvers();
  const active = new PublisherApplication({ store: f.app.store, vault: f.vault, tools: async () => {
    started.resolve(); await gate.promise; throw new Error('synthetic local tools failure');
  } });
  const pending = active.submitRelease(release.batch_id, plan.approval_plan);
  await started.promise;
  try {
    assert.throws(() => active.removeAccount('tencent', 'tencent-main'), /正在执行的提交或未决请求/);
    assert.throws(() => f.app.removeAccount('tencent', 'tencent-main'), /正在执行的提交或未决请求/);
    active.removeAccount('honor', 'honor-main');
  }
  finally { gate.resolve(); }
  await assert.rejects(pending, /synthetic local tools failure/);
  active.removeAccount('tencent', 'tencent-main');
  assert.deepEqual(active.accounts(), []);
});

test('CLI account removal confirms once and cancellation retains the account', async t => {
  const f = fixture(t), output = [];
  const profile = { channel: 'honor', account: 'honor-unused', client_id: 'unused-client' };
  f.app.saveAccount(profile);
  const terminal = answer => ({ ask: async label => { assert.equal(label, '输入 yes 移除'); return answer; },
    write() {}, close() {}, suspend() {} });
  await assert.rejects(run(['account', 'remove', 'honor', 'honor-unused'], { application: f.app, terminal: terminal('no'), output: text => output.push(text) }), /已取消/);
  assert.equal(f.app.accounts().some(row => row.account === 'honor-unused'), true);
  await assert.rejects(run(['account', 'remove', 'tencent', 'tencent-main'], { application: f.app, terminal: terminal('yes'), output: text => output.push(text) }), /仍被应用选用/);
  assert.equal(await run(['account', 'remove', 'honor', 'honor-unused'], { application: f.app, terminal: terminal('yes'), output: text => output.push(text) }), 0);
  assert.equal(f.app.accounts().some(row => row.account === 'honor-unused'), false);
  assert.match(output.join(''), /账号已移除/);
});

test('a credential cleanup failure leaves the account visible and its credential file intact', t => {
  const f = fixture(t);
  f.app.saveAccount({ channel: 'honor', account: 'honor-unused', client_id: 'unused-client' });
  f.vault.save('honor', 'honor-unused', { client_secret: 'synthetic-retained' });
  const locked = new PublisherApplication({ root: f.root, vault: new CredentialVault(f.root, {
    get() {}, put() {}, delete() { throw new Error('synthetic keychain locked'); },
  }) });
  try {
    assert.throws(() => locked.removeAccount('honor', 'honor-unused'), /synthetic keychain locked/);
    assert.equal(f.vault.has('honor', 'honor-unused'), true);
    assert.equal(locked.accounts().some(row => row.account === 'honor-unused'), true);
    assert.equal(f.app.store.accountRemoved('honor', 'honor-unused'), false);
  } finally { locked.close(); }
});

test('account removal detects another submitting process and ignores its abandoned execution marker after exit', { timeout: 10000 }, async t => {
  const f = fixture(t);
  const { release } = await f.app.prepareRelease(f.input);
  f.app.saveApp({ ...f.app.app('demo'), channels: [] }, true);
  const script = `import { Store } from ${JSON.stringify(new URL('../build/state.js', import.meta.url).href)};
    const store = new Store(process.argv[1]);
    store.beginSubmission(process.argv[2], ['tencent']);
    process.stdout.write('ready'); process.stdin.resume();`;
  const child = spawn(process.execPath, ['--input-type=module', '-e', script, f.root, release.batch_id], { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => { child.kill(); });
  await once(child.stdout, 'data');
  assert.throws(() => f.app.removeAccount('tencent', 'tencent-main'), /正在执行的提交或未决请求/);
  const exited = once(child, 'exit'); child.kill(); await exited;
  f.app.removeAccount('tencent', 'tencent-main');
  assert.deepEqual(f.app.accounts(), []);
});
