import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { PublisherApplication } from '../build/application.js';
import { CredentialVault } from '../build/vault.js';
import { startWebServer } from '../build/web.js';

test('browser follows a real shared release across navigation without another submission', {
  skip: process.env.PUBLISHER_BROWSER_TESTS !== '1', timeout: 60000,
}, async t => {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-web-journey-'));
  const keys = new Map();
  const vault = new CredentialVault(root, { get: id => keys.has(id) ? Buffer.from(keys.get(id)) : undefined,
    put: (id, value) => keys.set(id, Buffer.from(value)), delete: id => keys.delete(id) });
  let finish, began, submissions = 0, browser, web;
  const gate = new Promise(resolve => { finish = resolve; });
  const started = new Promise(resolve => { began = resolve; });
  const publisher = new PublisherApplication({ root, vault,
    tools: { inspect: () => ({ package_name: 'com.example.app', version_name: '2.0', version_code: '2', signer_sha256: 'a'.repeat(64), abis: ['arm64-v8a'] }) },
    fetcher: async (url, init) => {
      const pathname = new URL(url).pathname;
      if (init.body?.[Symbol.asyncIterator]) await new Response(init.body).arrayBuffer();
      if (pathname === '/upload') return new Response(null, { status: 200 });
      if (pathname.endsWith('query_app_detail')) return Response.json({ ret: 0, pkg_name: 'com.example.app', version_code: 1, app_name: 'Synthetic', introduce: 'Synthetic' });
      if (pathname.endsWith('get_file_upload_info')) {
        const now = Math.floor(Date.now() / 1000);
        return Response.json({ ret: 0, pre_sign_url: `https://example.cos.ap-shanghai.myqcloud.com/upload?q-sign-time=${now}%3B${now + 3600}`, serial_number: 'synthetic' });
      }
      if (pathname.endsWith('update_app')) { submissions++; began(); await gate; return Response.json({ ret: 0 }); }
      if (pathname.endsWith('query_app_update_status')) return Response.json({ ret: 0, audit_status: 3 });
      throw new Error('unexpected synthetic request');
    },
  });
  t.after(async () => { finish(); await browser?.close(); await web?.close(); publisher.close(); fs.rmSync(root, { recursive: true, force: true }); });
  publisher.saveApp({ schema_version: 2, project_id: 'reader', display_name: '示例阅读', package_name: 'com.example.app',
    channels: [{ channel: 'tencent', account: 'tencent-main', app_id: '456', client_id: 'synthetic-user' }] });
  await publisher.updateAccount('tencent', 'tencent-main', { api_secret: 'synthetic' });
  web = await startWebServer({ invoke: (action, payload) => action === 'doctor'
    ? Promise.resolve({ ready: true, version: '0.0.0-test', runtime: process.version, platform: 'darwin', arch: 'arm64', checks: [] })
    : publisher.invoke(action, payload) }, { root });
  browser = await chromium.launch({ ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}), headless: true });
  const page = await browser.newPage();
  const screenshot = async name => {
    const directory = process.env.PUBLISHER_BROWSER_SCREENSHOTS;
    if (directory) { fs.mkdirSync(directory, { recursive: true }); await page.screenshot({ path: path.join(directory, name + '.png'), fullPage: true }); }
  };
  const errors = []; page.on('pageerror', error => errors.push(error.message));
  await page.goto(web.url);
  await page.getByRole('heading', { name: '示例阅读', exact: true }).waitFor();
  await screenshot('application');
  await page.getByRole('button', { name: '发新版本', exact: true }).click();
  await page.locator('input[type="file"][accept*=".apk"]').first().setInputFiles({ name: 'synthetic.apk', mimeType: 'application/vnd.android.package-archive', buffer: Buffer.from('synthetic APK content') });
  await page.getByLabel(/更新说明/).first().fill('改进阅读体验，修复已知问题。 <b data-reader-note>说明原文</b>');
  await page.getByText('已自动保存到本机', { exact: true }).waitFor();
  await page.reload();
  assert.equal(await page.getByLabel(/更新说明/).first().inputValue(), '改进阅读体验，修复已知问题。 <b data-reader-note>说明原文</b>');
  assert.match(await page.locator('main').innerText(), /synthetic\.apk/);
  await page.getByRole('button', { name: '检查材料并核对提交内容' }).click();
  const submit = page.getByRole('button', { name: '确认上传并提交审核' });
  await submit.waitFor({ state: 'visible' });
  assert.equal(submissions, 0);
  assert.match(await page.locator('main').innerText(), /审核通过后.*上架/);
  assert.match(await page.locator('main').innerText(), /<b data-reader-note>/);
  assert.equal(await page.locator('[data-reader-note]').count(), 0);
  const confirmation = page.getByRole('checkbox', { name: /核对|确认/ });
  if (await confirmation.count()) await confirmation.first().check();
  await submit.click();
  let timeout;
  try { await Promise.race([started, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('submission did not start')), 15000); })]); }
  finally { clearTimeout(timeout); }
  assert.equal(submissions, 1);
  const [release] = publisher.releases('reader');
  assert.equal(release.has_writes, true);
  await page.goto(web.url + '/#/accounts');
  await page.getByRole('heading', { name: /账号/ }).first().waitFor();
  finish();
  await page.goto(web.url);
  await page.getByRole('heading', { name: '示例阅读', exact: true }).waitFor();
  await page.getByText(/提交完成|商店已接收|已提交/).first().waitFor();
  await page.reload();
  await page.getByRole('heading', { name: '示例阅读', exact: true }).waitFor();
  assert.equal(submissions, 1);
  assert.equal(publisher.release(release.batch_id).all_accepted, true);
  assert.equal(publisher.store.operations(release.batch_id, 'tencent').filter(op => op.step === 'submit').length, 1);
  assert.equal(fs.existsSync(path.join(root, 'web-drafts', 'release', 'reader.json')), false);
  await page.goto(web.url + '/#/release/' + release.batch_id);
  await page.getByRole('heading', { name: /示例阅读.*2\.0/ }).waitFor();
  await screenshot('release-result');
  assert.deepEqual(errors, []);
  assert.equal(await page.evaluate(() => JSON.stringify(localStorage).includes('synthetic')), false);
});
