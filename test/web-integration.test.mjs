import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { PublisherApplication } from '../build/application.js';
import { CredentialVault } from '../build/vault.js';
import { startWebServer } from '../build/web.js';

test('Xiaomi onboarding accepts an email developer account and opens credential entry', {
  skip: process.env.PUBLISHER_BROWSER_TESTS !== '1', timeout: 30000,
}, async t => {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-xiaomi-onboarding-'));
  const keys = new Map();
  const vault = new CredentialVault(root, { get: id => keys.get(id), put: (id, value) => keys.set(id, Buffer.from(value)), delete: id => keys.delete(id) });
  const publisher = new PublisherApplication({ root, vault });
  const web = await startWebServer({ invoke: (action, payload) => action === 'doctor'
    ? Promise.resolve({ ready: true, version: 'test', checks: [] }) : publisher.invoke(action, payload) }, { root });
  let browser;
  t.after(async () => { await browser?.close(); await web.close(); publisher.close(); fs.rmSync(root, { recursive: true, force: true }); });
  browser = await chromium.launch({ headless: true, ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}) });
  const page = await browser.newPage();
  await page.goto(web.url + '/#/setup/new');
  await page.getByLabel('应用名称', { exact: true }).fill('示例应用');
  await page.getByLabel('Android 包名', { exact: true }).fill('com.example.app');
  await page.getByRole('button', { name: '创建应用', exact: true }).click();
  await page.getByRole('link', { name: '配置商店', exact: true }).first().click();
  await page.locator('[data-setup-select=xiaomi]').check();
  await page.getByRole('button', { name: /下一步：应用与账号/ }).click();
  await page.locator('#channel-client_id').fill('developer+release@example.com');
  await page.getByText('使用文件路径', { exact: true }).click();
  await page.locator('#channel-icon_path').fill('/private/synthetic/../icon.png');
  await page.locator('#channel-privacy_url').fill('https://example.com/privacy');
  await page.getByRole('button', { name: '保存并填写凭据', exact: true }).click();
  await page.getByText(/图片路径无效/).waitFor({ timeout: 3000 });
  await page.locator('#channel-icon_path').fill('/private/synthetic/icon.png');
  await page.locator('#channel-privacy_url').fill('https://example.com/privacy?invalid=1');
  await page.getByRole('button', { name: '保存并填写凭据', exact: true }).click();
  await page.getByText(/请填写不带查询参数或片段的 HTTPS 隐私政策网页地址/).waitFor();
  assert.equal(await page.locator('#channel-client_id').inputValue(), 'developer+release@example.com');
  await page.locator('#channel-privacy_url').fill('https://example.com/privacy');
  await page.getByRole('button', { name: '保存并填写凭据', exact: true }).click();
  await page.locator('#credentials-form').waitFor({ state: 'visible', timeout: 3000 });
  assert.equal(publisher.apps()[0].channels[0].client_id, 'developer+release@example.com');
});

test('package-based channels accept a valid Android package longer than 128 characters', {
  skip: process.env.PUBLISHER_BROWSER_TESTS !== '1', timeout: 30000,
}, async t => {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-long-package-'));
  const keys = new Map();
  const vault = new CredentialVault(root, { get: id => keys.get(id), put: (id, value) => keys.set(id, Buffer.from(value)), delete: id => keys.delete(id) });
  const publisher = new PublisherApplication({ root, vault });
  const web = await startWebServer({ invoke: (action, payload) => action === 'doctor'
    ? Promise.resolve({ ready: true, version: 'test', checks: [] }) : publisher.invoke(action, payload) }, { root });
  let browser;
  t.after(async () => { await browser?.close(); await web.close(); publisher.close(); fs.rmSync(root, { recursive: true, force: true }); });
  browser = await chromium.launch({ headless: true, ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}) });
  const page = await browser.newPage(), pkg = 'com.' + 'example'.repeat(19) + '.app';
  await page.goto(web.url + '/#/setup/new');
  await page.getByLabel('应用名称', { exact: true }).fill('示例应用');
  await page.getByLabel('Android 包名', { exact: true }).fill(pkg);
  await page.getByRole('button', { name: '创建应用', exact: true }).click();
  await page.getByRole('link', { name: '配置商店', exact: true }).first().click();
  await page.locator('[data-setup-select=oppo]').check();
  await page.getByRole('button', { name: /下一步：应用与账号/ }).click();
  await page.locator('#channel-client_id').fill('invalid client');
  await page.getByRole('button', { name: '保存并填写凭据', exact: true }).click();
  await page.getByText(/API 客户端编号.*字母、数字.*128/).waitFor({ timeout: 3000 });
  await page.locator('#channel-client_id').fill('example-client');
  await page.getByRole('button', { name: '保存并填写凭据', exact: true }).click();
  await page.locator('#credentials-form').waitFor({ state: 'visible', timeout: 3000 });
  assert.equal(publisher.apps()[0].channels[0].app_id, pkg);
});

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
  await page.getByRole('button', { name: '发布新版本', exact: true }).click();
  await page.locator('input[type="file"][accept*=".apk"]').first().setInputFiles({ name: 'synthetic.apk', mimeType: 'application/vnd.android.package-archive', buffer: Buffer.from('synthetic APK content') });
  await page.getByLabel(/更新说明/).first().fill('改进阅读体验，修复已知问题。 <b data-reader-note>说明原文</b>');
  await page.getByText('草稿已保存', { exact: true }).waitFor();
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

test('Web independently creates accounts and applications, reuses credentials, and keeps application access separate', {
  skip: process.env.PUBLISHER_BROWSER_TESTS !== '1', timeout: 60000,
}, async t => {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-independent-onboarding-'));
  const keys = new Map(), requests = [], errors = [];
  const vault = new CredentialVault(root, { get: id => keys.has(id) ? Buffer.from(keys.get(id)) : undefined,
    put: (id, value) => keys.set(id, Buffer.from(value)), delete: id => keys.delete(id) });
  const publisher = new PublisherApplication({ root, vault, fetcher: async (url, init) => {
    assert.ok(new URL(url).pathname.endsWith('query_app_detail'), 'only application access checks are allowed');
    const fields = new URLSearchParams(String(init.body)), pkg = fields.get('pkg_name');
    requests.push(pkg);
    return Response.json({ ret: 0, pkg_name: pkg === 'com.example.notes' ? 'com.example.denied' : pkg,
      version_code: 1, app_name: 'Synthetic', introduce: 'Synthetic' });
  } });
  const web = await startWebServer(publisher, { root }); let browser;
  t.after(async () => { await browser?.close(); await web.close(); publisher.close(); fs.rmSync(root, { recursive: true, force: true }); });
  browser = await chromium.launch({ headless: true, ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.setDefaultTimeout(10000); page.on('pageerror', error => errors.push(error.message));
  const screenshot = async name => {
    const directory = process.env.PUBLISHER_BROWSER_SCREENSHOTS;
    if (directory) { fs.mkdirSync(directory, { recursive: true }); await page.screenshot({ path: path.join(directory, name + '.png'), fullPage: true }); }
  };
  await page.goto(web.url + '/#/accounts');
  await page.getByRole('button', { name: '添加账号', exact: true }).first().click();
  await page.getByLabel('商店', { exact: true }).selectOption('tencent');
  await page.getByLabel('账号备注', { exact: true }).fill('tencent-company');
  await page.locator('#new-account-client').fill('synthetic-user');
  await screenshot('new-account');
  await page.getByRole('button', { name: '保存账号并填写凭据', exact: true }).click();
  await page.locator('#credential-api_secret').fill('synthetic-shared-secret');
  const credentialForm = await page.locator('#credentials-form').elementHandle();
  await page.getByRole('button', { name: '保存凭据', exact: true }).click();
  await page.locator('#credentials-form').waitFor({ state: 'detached' });
  assert.equal(await credentialForm.evaluate(form => [...form.querySelectorAll('input,textarea')].every(input => !input.value)), true);
  await credentialForm.dispose();
  assert.equal(publisher.apps().length, 0); assert.deepEqual(requests, []);
  assert.deepEqual(publisher.accounts()[0].apps, []); assert.equal(publisher.accounts()[0].saved, true);
  await screenshot('independent-account');
  const create = async (name, pkg) => {
    await page.goto(web.url + '/#/setup/new');
    await page.getByLabel('应用名称', { exact: true }).fill(name);
    await page.getByLabel('Android 包名', { exact: true }).fill(pkg);
    await screenshot('create-application');
    await page.getByRole('button', { name: '创建应用', exact: true }).click();
    await page.getByRole('heading', { name, exact: true }).waitFor();
    const app = publisher.apps().find(row => row.package_name === pkg);
    assert.deepEqual(app.channels, []);
    await screenshot('application-without-channels');
    return app;
  };
  const configure = async appId => {
    await page.getByRole('link', { name: '配置商店', exact: true }).first().click();
    await page.locator('[data-setup-select=tencent]').check();
    await page.getByRole('button', { name: /下一步：应用与账号/ }).click();
    await page.locator('#channel-app_id').fill('10001');
    assert.equal(await page.locator('#account-choice').inputValue(), 'tencent-company');
    assert.equal(await page.locator('#channel-client_id').inputValue(), 'synthetic-user');
    await screenshot('configure-application-channel');
    await page.getByRole('button', { name: '保存并检查账号访问', exact: true }).click();
    await page.waitForFunction(id => state.apps.some(app => app.project_id === id && app.channels.length === 1), appId);
    assert.equal(publisher.app(appId).channels[0].account, 'tencent-company');
  };
  const reader = await create('阅读助手', 'com.example.reader');
  await configure(reader.project_id);
  await page.getByRole('heading', { name: '商店配置完成', exact: true }).waitFor();
  await page.getByRole('link', { name: '进入应用', exact: true }).click();
  await page.getByRole('heading', { name: '阅读助手', exact: true }).waitFor();
  await screenshot('application-channel-ready');
  assert.equal(await page.locator('.channel-readiness').count(), 0);
  assert.equal(await page.locator('.app-store-summary').getByText('应用宝', { exact: true }).isVisible(), true);
  assert.equal(await page.getByText('账号：tencent-company', { exact: true }).count(), 0);
  await page.getByRole('link', { name: '管理', exact: true }).click();
  await page.getByRole('heading', { name: '应用设置', exact: true }).waitFor();
  assert.match(await page.locator('.breadcrumbs').textContent(), /阅读助手.*应用设置/);
  assert.equal(await page.getByRole('navigation', { name: '应用设置', exact: true }).getByRole('link', { name: '发布商店', exact: true }).getAttribute('aria-current'), 'page');
  await screenshot('application-store-settings');
  await page.getByRole('link', { name: '编辑配置', exact: true }).click();
  await page.getByRole('heading', { name: '编辑应用宝配置', exact: true }).waitFor();
  assert.equal(await page.locator('#channel-app_id').inputValue(), '10001');
  assert.equal(await page.locator('#account-choice').inputValue(), 'tencent-company');
  assert.equal(await page.getByRole('button', { name: '返回选择商店', exact: true }).count(), 0);
  await screenshot('edit-application-store');
  await page.getByRole('button', { name: '重新检查账号访问', exact: true }).click();
  await page.getByRole('heading', { name: '应用宝配置已保存', exact: true }).waitFor();
  await page.getByRole('link', { name: '返回发布商店', exact: true }).click();
  await page.getByRole('navigation', { name: '应用设置', exact: true }).getByRole('link', { name: '基本资料', exact: true }).click();
  assert.equal(await page.getByLabel('应用名称', { exact: true }).inputValue(), '阅读助手');
  await page.getByRole('navigation', { name: '应用设置', exact: true }).getByRole('link', { name: '发布商店', exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await screenshot('mobile-application-store-settings');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('link', { name: '添加商店', exact: true }).click();
  await page.locator('[data-setup-select=honor]').check();
  await page.getByRole('button', { name: /下一步：应用与账号/ }).click();
  await page.locator('#channel-app_id').fill('20001');
  await page.getByText('草稿已保存', { exact: true }).waitFor();
  await page.goto(web.url + '/#/setup/' + reader.project_id);
  await page.getByLabel('应用名称', { exact: true }).fill('阅读助手新版名称');
  await page.getByRole('button', { name: '保存应用资料', exact: true }).click();
  await page.getByRole('heading', { name: '阅读助手新版名称', exact: true }).waitFor();
  assert.equal(publisher.app(reader.project_id).channels.length, 1);
  await page.getByRole('link', { name: '继续配置商店', exact: true }).click();
  await page.getByText('阅读助手新版名称 · com.example.reader', { exact: true }).waitFor();
  assert.equal(await page.locator('#channel-app_id').inputValue(), '20001');
  const notes = await create('笔记助手', 'com.example.notes');
  await configure(notes.project_id);
  await page.getByRole('alert').filter({ hasText: /无法读取应用资料/ }).waitFor();
  assert.equal(publisher.accounts().length, 1);
  const overviews = publisher.appOverviews();
  assert.equal(overviews.find(row => row.app_id === reader.project_id).channels[0].connected, true);
  assert.equal(overviews.find(row => row.app_id === notes.project_id).channels[0].connected, false);
  assert.equal(vault.get('tencent', 'tencent-company').api_secret, 'synthetic-shared-secret');
  assert.equal(publisher.store.batchIds().length, 0);
  assert.equal(await page.evaluate(() => JSON.stringify(state).includes('synthetic-shared-secret')), false);
  await page.setViewportSize({ width: 390, height: 844 });
  await screenshot('mobile-application-channel');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  publisher.saveAccount({ channel: 'honor', account: 'honor-unused', client_id: 'synthetic-unused-client' });
  vault.save('honor', 'honor-unused', { client_secret: 'synthetic-unused-secret' });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(web.url + '/#/accounts');
  const shared = page.locator('tr').filter({ hasText: '账号备注：tencent-company' });
  assert.equal(await shared.getByRole('button', { name: '移除账号', exact: true }).isDisabled(), true);
  assert.match(await shared.textContent(), /先在应用设置中更换账号或移除对应商店/);
  const unused = page.locator('tr').filter({ hasText: '账号备注：honor-unused' });
  await unused.getByRole('button', { name: '移除账号', exact: true }).waitFor();
  page.once('dialog', dialog => dialog.dismiss());
  await unused.getByRole('button', { name: '移除账号', exact: true }).click();
  assert.equal(vault.has('honor', 'honor-unused'), true);
  await screenshot('account-removal');
  page.once('dialog', async dialog => { assert.match(dialog.message(), /历史发布记录会保留/); assert.match(dialog.message(), /真实账号不受影响/); await dialog.accept(); });
  await unused.getByRole('button', { name: '移除账号', exact: true }).click();
  await unused.waitFor({ state: 'detached' });
  assert.equal(vault.has('honor', 'honor-unused'), false);
  assert.equal(publisher.accounts().length, 1);
  await page.reload();
  await shared.waitFor();
  assert.equal(await unused.count(), 0);
  assert.deepEqual(errors, []);
});
