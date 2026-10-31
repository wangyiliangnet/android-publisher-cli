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
    channel: 'xiaomi', account: 'xiaomi-main', client_id: xiaomi.client_id, apps: [], app_names: [], saved: false,
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
