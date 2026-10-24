import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { generateKeyPairSync } from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import { digest } from '../build/model.js';
import { hashFile, md5File } from '../build/private-file.js';
import { Store } from '../build/state.js';
import { channelAdapter } from '../build/channels/index.js';
import { ChannelHTTP } from '../build/channels/http.js';

const temp = () => fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-channel-'));
const success = value => new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } });
const testKeys = generateKeyPairSync('rsa', { modulusLength: 2048, publicKeyEncoding: { type: 'spki', format: 'pem' },
  privateKeyEncoding: { type: 'pkcs8', format: 'pem' } });

function fixture(channel, root) {
  const file = path.join(root, 'synthetic.apk');
  fs.writeFileSync(file, 'synthetic APK bytes');
  const hashed = hashFile(file);
  const artifact = { path: file, filename: 'synthetic.apk', sha256: hashed.sha256, size: hashed.size,
    signer_sha256: 'a'.repeat(64), package_name: 'com.example.app', version_name: '2.0', version_code: '2', abis: ['arm64-v8a'] };
  const icon = path.join(root, 'icon.png');
  fs.writeFileSync(icon, 'synthetic icon');
  const iconHash = hashFile(icon);
  const target = { channel, account: `${channel}-account`,
    app_id: { huawei: '123', honor: '123', xiaomi: 'com.example.app', oppo: 'com.example.app', vivo: 'com.example.app', tencent: '456' }[channel],
    client_id: { honor: 'honor-client', xiaomi: 'xiaomi-user', oppo: 'oppo-client', vivo: 'vivo-access', tencent: 'tencent-user' }[channel],
    artifact,
    ...(channel === 'xiaomi' ? { icon_path: icon, icon_sha256: iconHash.sha256, icon_size: iconHash.size,
      privacy_url: 'https://example.invalid/privacy' } : {}) };
  const body = { schema_version: 2, batch_id: `${channel}-batch`, project_id: 'synthetic', package_name: 'com.example.app',
    version_name: '2.0', version_code: '2', signer_sha256: 'a'.repeat(64), release_notes: 'Synthetic update notes',
    release_policy: 'after_approval', targets: [target] };
  return { batch: { ...body, digest: digest(body), created_at: new Date().toISOString() }, target,
    md5: md5File(file, hashed.sha256) };
}

function mock(channel, fixture) {
  const calls = [];
  let submitted = false;
  const fetcher = async (url, init) => {
    const u = new URL(url);
    calls.push({ method: init.method, path: u.pathname, host: u.hostname });
    if (init.body instanceof ReadableStream || init.body?.[Symbol.asyncIterator])
      await new Response(init.body).arrayBuffer();
    if (u.pathname === '/upload' && init.method === 'PUT') return new Response(null, { status: 200 });
    if (channel === 'huawei') {
      const wrap = content => success({ ret: { code: 0 }, ...content });
      if (u.pathname.endsWith('appid-list')) return wrap({ appids: [{ value: '123' }] });
      if (u.pathname.endsWith('app-info')) return wrap({ appInfo: { versionCode: submitted ? 2 : 1,
        versionNumber: submitted ? '2.0' : '1.0', versionId: submitted ? 'v2' : 'v1', releaseState: submitted ? 0 : 4,
        onShelfVersionCode: submitted ? 2 : 1 },
        languages: [{ lang: 'zh-CN', appName: 'Example', appDesc: 'Introduction', briefInfo: 'Brief', newFeatures: 'Old' }] });
      if (u.pathname.endsWith('for-obs')) return wrap({ urlInfo: { url: 'https://connect-api.cloud.huawei.com/upload',
        objectId: 'object-1', method: 'PUT', headers: { Authorization: 'OBS synthetic', 'X-Amz-Content-Sha256': fixture.target.artifact.sha256,
          'X-Amz-Date': '20261024T000000Z', Host: 'connect-api.cloud.huawei.com', 'User-Agent': 'synthetic',
          'Content-Type': 'application/octet-stream' } } });
      if (u.pathname.endsWith('app-submit')) submitted = true;
      return wrap({});
    }
    if (channel === 'honor') {
      if (u.pathname === '/auth/token') return success({ token_type: 'Bearer', expires_in: 3600, access_token: 'synthetic-token' });
      if (u.pathname.endsWith('get-app-id')) return success({ code: 0, data: [{ appId: 123, packageName: 'com.example.app' }] });
      if (u.pathname.endsWith('get-app-detail')) return success({ code: 0, data: { basicInfo: { appId: 123, packageName: 'com.example.app' },
        releaseInfo: { versionCode: 1 }, languageInfo: [{ languageId: 'zh-CN', appName: 'Example', intro: 'Introduction',
          briefIntro: 'Brief', newFeature: 'Old' }] } });
      if (u.pathname.endsWith('get-file-upload-url')) return success({ code: 0, data: [{ fileName: 'synthetic.apk', objectId: 789,
        expireTime: Math.floor(Date.now() / 1000) + 3600,
        uploadUrl: 'https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=789' }] });
      if (u.pathname.endsWith('submit-audit')) return success({ code: 0, data: 'release-1' });
      if (u.pathname.endsWith('get-audit-result')) return success({ code: 0, data: [{ appId: 123, releaseId: 'release-1', auditResult: 1 }] });
      return success({ code: 0 });
    }
    if (channel === 'xiaomi') {
      if (u.pathname.endsWith('/query')) return success({ result: 0, packageInfo: { versionCode: 1,
        packageName: 'com.example.app', appName: 'Example' }, updateVersion: true });
      return success({ result: 0 });
    }
    if (channel === 'oppo') {
      if (u.pathname.endsWith('/token')) return success({ errno: 0, data: { access_token: 'synthetic-token',
        expire_in: Math.floor(Date.now() / 1000) + 3600 } });
      if (u.pathname.endsWith('/app/info')) return success({ errno: 0, data: { pkg_name: 'com.example.app', app_id: 'remote-oppo',
        version_code: submitted ? 2 : 1, version_name: submitted ? '2.0' : '1.0',
        app_name: 'Example', second_category_id: 1, third_category_id: 2, summary: 'Summary',
        detail_desc: 'A long synthetic description for this fixture.', update_desc: 'Old notes',
        privacy_source_url: 'https://example.invalid/privacy', icon_url: 'https://example.invalid/icon',
        pic_url: 'https://example.invalid/a,https://example.invalid/b', test_desc: 'Synthetic test',
        copyright_url: 'https://example.invalid/copyright', business_username: 'Example',
        business_email: 'example@example.invalid', business_mobile: '00000000000', age_level: 3,
        adaptive_equipment: 4, apk_md5: submitted ? fixture.md5 : '', version_id: submitted ? 'v2' : 'v1',
        audit_status: submitted ? 111 : 0, state: submitted ? 1 : 0 } });
      if (u.pathname.endsWith('get-upload-url')) return success({ errno: 0, data: {
        upload_url: 'https://oop-openapi-cn.heytapmobi.com/upload', sign: 'synthetic-sign' } });
      if (u.pathname === '/upload') return success({ errno: 0, data: { url: 'https://example.invalid/file.apk',
        file_size: fixture.target.artifact.size, md5: fixture.md5, file_extension: 'apk', id: 'file1' } });
      if (u.pathname.endsWith('/app/upd')) { submitted = true; return success({ errno: 0, data: { success: true } }); }
    }
    if (channel === 'vivo') {
      if (u.pathname === '/router/rest' && init.headers['Content-Type']?.startsWith('multipart')) return success({ code: 0, subCode: '0',
        data: { packageName: 'com.example.app', versionCode: 2, versionName: '2.0',
          serialnumber: 'vivo-serial', fileMd5: fixture.md5 } });
      const query = new URLSearchParams(String(init.body));
      if (query.get('method') === 'app.query.details') return success({ code: 0, subCode: '0', data: {
        packageName: 'com.example.app', versionCode: submitted ? 2 : 1, versionName: submitted ? '2.0' : '1.0',
        compatibleDevice: 1, ecopyright: 'https://example.invalid/copyright',
        detailDesc: 'A synthetic description long enough to satisfy the fifty character minimum in the release metadata.',
        mainTitle: 'Example', updateDesc: 'Old notes', status: submitted ? 3 : 1, saleStatus: submitted ? 1 : 0 } });
      submitted = true;
      return success({ code: 0, subCode: '0' });
    }
    if (channel === 'tencent') {
      if (u.pathname.endsWith('query_app_detail')) return success({ ret: 0, pkg_name: 'com.example.app',
        version_code: 1, app_name: 'Example', introduce: 'Introduction' });
      if (u.pathname.endsWith('get_file_upload_info')) return success({ ret: 0,
        pre_sign_url: `https://example.cos.ap-shanghai.myqcloud.com/upload?q-sign-time=${Math.floor(Date.now() / 1000)}%3B${Math.floor(Date.now() / 1000) + 3600}`,
        serial_number: 'tencent-serial' });
      if (u.pathname.endsWith('query_app_update_status')) return success({ ret: 0, audit_status: 3 });
      return success({ ret: 0 });
    }
    throw new Error(`unexpected synthetic path: ${u.pathname}`);
  };
  return { fetcher, calls };
}

for (const channel of ['huawei', 'honor', 'xiaomi', 'oppo', 'vivo', 'tencent']) {
  test(`${channel} uses read preflight, logged writes, and separate observation`, async () => {
    const root = temp();
    try {
      const fixtureValue = fixture(channel, root);
      const store = new Store(root);
      store.reservePacing = () => 0;
      store.saveBatch(fixtureValue.batch);
      const credentials = { huawei: { service_account_json: JSON.stringify({ key_id: 'synthetic', sub_account: 'synthetic', private_key: testKeys.privateKey }) }, honor: { client_secret: 'synthetic-secret' },
        xiaomi: { api_secret: 'synthetic-secret', public_key_pem: testKeys.publicKey },
        oppo: { client_secret: 'synthetic-secret' }, vivo: { access_secret: 'synthetic-secret' },
        tencent: { api_secret: 'synthetic-secret' } }[channel];
      const network = mock(channel, fixtureValue);
      const adapter = channelAdapter(fixtureValue.batch, fixtureValue.target, credentials, store, network.fetcher);
      const observed = await adapter.preflight();
      store.savePreflight(fixtureValue.batch.batch_id, observed);
      for (const step of adapter.steps) {
        const op = store.beginOperation(fixtureValue.batch.batch_id, channel, step, digest(step));
        const result = await adapter.execute(step, op.id);
        store.finishOperation(op, 'confirmed_applied', result.ref, result.material);
      }
      store.setSubmitted(fixtureValue.batch.batch_id, channel, store.latestOperation(fixtureValue.batch.batch_id, channel, adapter.steps.at(-1)).remote_ref);
      const status = await adapter.observe(store.target(fixtureValue.batch.batch_id, channel).remote_ref);
      store.saveObservation(fixtureValue.batch.batch_id, channel, status);
      assert.equal(store.summary(fixtureValue.batch.batch_id).accepted_targets, 1);
      assert.equal(store.operations(fixtureValue.batch.batch_id).length, adapter.steps.length);
      assert.ok(network.calls.some(call => call.method === 'POST' || call.method === 'PUT'));
      store.close();
    } finally { fs.rmSync(root, { recursive: true, force: true }); }
  });
}

test('transport refuses a write without a persisted intent', async () => {
  const client = new ChannelHTTP('huawei', () => { throw new Error('network should not be reached'); });
  await assert.rejects(client.json('/api/publish/v2/app-submit', 'POST', 'write', {}, 'fake'), /unlogged_write/);
});

test('invalid Xiaomi test accounts are rejected before a business intent', async () => {
  const root = temp();
  try {
    const fixtureValue = fixture('xiaomi', root);
    const store = new Store(root);
    store.saveBatch(fixtureValue.batch);
    const adapter = channelAdapter(fixtureValue.batch, fixtureValue.target,
      { api_secret: 'synthetic', public_key_pem: testKeys.publicKey,
        test_account_json: '{"zh_CN":{"accounts":[{"t":3,"a":"user","p":"pass","c":""}],"auditNotes":""}}' },
      store, () => { throw new Error('network should not be reached'); });
    await assert.rejects(adapter.waitBeforeStep('push'), /test accounts are invalid/);
    assert.equal(store.operations(fixtureValue.batch.batch_id).length, 0);
    store.close();
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
