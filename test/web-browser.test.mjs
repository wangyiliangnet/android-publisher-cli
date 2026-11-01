import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { startWebServer } from '../build/web.js';
import { CredentialInputError } from '../build/credentials.js';

// Opt in with a locally installed Playwright module and a fresh browser profile.
// No real credentials, account state, or storefront requests are used.
test('Web credential entry preserves multiline materials and clears every completed or abandoned form', {
  skip: process.env.PUBLISHER_BROWSER_TESTS !== '1' ? 'set PUBLISHER_BROWSER_TESTS=1 with Playwright available' : false,
  timeout: 120000,
}, async t => {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const channels = ['huawei', 'honor', 'xiaomi', 'oppo', 'vivo', 'tencent'];
  const app = { schema_version: 2, project_id: 'synthetic-app', package_name: 'com.example.app', channels: channels.map(channel => ({
    channel, account: channel + '-main', app_id: '10001', client_id: 'synthetic-public-id',
  })) };
  const accounts = channels.map(channel => ({ channel, account: channel + '-main', apps: [app.project_id], saved: false }));
  const updates = [], errors = [], logs = [];
  let failNext = false, blockNext = false, nextIssue, unblock, repairs = 0, uploads = 0;
  const diagnostic = () => ({ ready: repairs > 0, version: '0.0.0-test', runtime: process.version, platform: 'darwin', arch: 'arm64',
    checks: [{ id: 'apk_checker', name: 'APK 检查组件', status: repairs > 0 ? 'ok' : 'missing', fixes: [] }] });
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-browser-'));
  const web = await startWebServer({ async invoke(action, payload) {
    if (action === 'apps.list') return { apps: [app] };
    if (action === 'accounts.list') return { accounts };
    if (action === 'accounts.update') {
      updates.push(structuredClone(payload));
      if (blockNext) { blockNext = false; await new Promise(resolve => { unblock = resolve; }); }
      if (nextIssue) { const issue = nextIssue; nextIssue = undefined; throw new CredentialInputError(issue); }
      if (failNext) { failNext = false; throw new Error('synthetic backend error ' + payload.credentials.client_secret); }
      accounts.find(row => row.channel === payload.channel).saved = true;
      return { updated: true, extra: 'must not be exposed in the HTTP response' };
    }
    if (action === 'doctor') return diagnostic();
    if (action === 'doctor.repair') { repairs++; return diagnostic(); }
    if (action === 'releases.list') return { releases: [] };
    throw new Error('unexpected synthetic browser action');
  } }, { root });
  let browser;
  t.after(async () => { unblock?.(); await browser?.close(); await web.close(); fs.rmSync(root, { recursive: true, force: true }); });
  browser = await chromium.launch({ headless: true,
    ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => logs.push(message.text()));
  page.on('request', request => { if (request.url().endsWith('/api/upload')) uploads++; });
  const row = channel => page.locator('tr').filter({ has: page.getByText(channel + '-main', { exact: true }) });
  const open = async channel => { await row(channel).getByRole('button', { name: /^(填写凭据|更新凭据)$/ }).click(); await page.locator('#credentials-form').waitFor(); };
  const saved = async channel => { await page.locator('#credentials-form').waitFor({ state: 'detached' }); await row(channel).getByRole('button', { name: '更新凭据', exact: true }).waitFor(); };
  const empty = handle => handle.evaluate(form => [...form.querySelectorAll('input,textarea')].every(control => control.value === ''));
  const noPersistence = async marker => {
    assert.equal(await page.evaluate(value => JSON.stringify(state).includes(value), marker), false);
    assert.equal(page.url().includes(marker), false);
    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
    assert.equal(logs.join('\n').includes(marker), false);
  };

  await page.goto(web.url + '/#/accounts');
  await open('honor');
  const honor = 'synthetic-honor-secret-A9';
  await page.locator('#credential-client_secret').fill(honor);
  assert.equal(await page.locator('#credential-client_secret').getAttribute('type'), 'password');
  await noPersistence(honor);
  await page.getByRole('button', { name: '显示内容', exact: true }).click();
  assert.equal(await page.locator('#credential-client_secret').getAttribute('type'), 'text');
  await page.getByRole('button', { name: '隐藏内容', exact: true }).click();
  blockNext = true;
  const submitted = await page.locator('#credentials-form').elementHandle();
  await page.getByRole('button', { name: '验证并保存', exact: true }).click();
  await page.locator('#credential-progress').waitFor({ state: 'visible' });
  assert.equal(await empty(submitted), true);
  assert.equal(await page.locator('#credential-save').isDisabled(), true);
  while (!unblock) await new Promise(resolve => setTimeout(resolve, 5));
  unblock();
  await saved('honor');
  assert.equal(await empty(submitted), true); await submitted.dispose();
  assert.equal(updates.at(-1).credentials.client_secret, honor);
  assert.deepEqual(updates.at(-1).files, {});

  await open('huawei');
  await page.locator('#credential-mode-service_account_json').selectOption('file');
  await page.locator('#credential-file-service_account_json').fill('/private/synthetic/service-account.json');
  await page.getByRole('button', { name: '验证并保存', exact: true }).click(); await saved('huawei');
  assert.deepEqual(updates.at(-1).files, { service_account_json: '/private/synthetic/service-account.json' });
  assert.deepEqual(updates.at(-1).credentials, {});
  await open('huawei');
  await page.locator('#credential-mode-service_account_json').selectOption('file');
  await page.locator('#credential-file-service_account_json').fill('/private/synthetic/wrong-permission.json');
  nextIssue = 'file';
  await page.getByRole('button', { name: '验证并保存', exact: true }).click();
  await page.locator('#credential-error').waitFor({ state: 'visible' });
  const fileHint = await page.locator('#credential-error').textContent();
  assert.match(fileHint, /当前用户/); assert.match(fileHint, /0600/); assert.match(fileHint, /绝对路径/); assert.match(fileHint, /粘贴/);
  assert.doesNotMatch(fileHint, /wrong-permission/);
  assert.equal(await page.locator('#credential-file-service_account_json').inputValue(), '');
  await page.locator('#credential-mode-service_account_json').selectOption('text');
  const privateLabel = ['PRIVATE', 'KEY'].join(' ');
  const syntheticPrivateKey = ['-----BEGIN ' + privateLabel + '-----', 'U1lOVEhFVElD', '-----END ' + privateLabel + '-----', ''].join('\n');
  const json = JSON.stringify({ key_id: 'synthetic-key-id', sub_account: 'synthetic-sub-account',
    private_key: syntheticPrivateKey }, null, 2) + '\n';
  await page.locator('#credential-text-service_account_json').fill(json);
  await noPersistence('synthetic-sub-account');
  assert.equal(await page.locator('#credential-text-service_account_json').evaluate(control => getComputedStyle(control).webkitTextSecurity), 'disc');
  await page.getByRole('button', { name: '显示内容', exact: true }).click();
  assert.equal(await page.locator('#credential-text-service_account_json').inputValue(), json);
  await page.getByRole('button', { name: '隐藏内容', exact: true }).click();
  const jsonForm = await page.locator('#credentials-form').elementHandle();
  await page.getByRole('button', { name: '验证并保存', exact: true }).click(); await saved('huawei');
  assert.equal(await empty(jsonForm), true); await jsonForm.dispose();
  assert.equal(updates.at(-1).credentials.service_account_json, json);

  await open('xiaomi');
  const pem = '-----BEGIN PUBLIC KEY-----\nU1lOVEhFVElD\n-----END PUBLIC KEY-----\n';
  const testAccount = '{\n  "username": "synthetic-user",\n  "password": "synthetic-test-password-C9"\n}\n';
  const xiaomiSecret = 'synthetic value';
  await page.locator('#credential-api_secret').fill(xiaomiSecret);
  await page.locator('#credentials-form').evaluate(form => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
  assert.match(await page.locator('#credential-error').textContent(), /API 公钥或证书 PEM/);
  assert.equal(await page.locator('#credential-api_secret').inputValue(), '');
  await page.locator('#credential-api_secret').fill(xiaomiSecret);
  await page.locator('#credential-mode-public_key_pem').selectOption('text');
  await page.locator('#credential-text-public_key_pem').fill(pem);
  await page.locator('.credential-optional summary').click();
  await page.locator('#credential-mode-test_account_json').selectOption('text');
  await page.locator('#credential-text-test_account_json').fill(testAccount);
  await page.getByRole('button', { name: '验证并保存', exact: true }).click(); await saved('xiaomi');
  assert.deepEqual(updates.at(-1).credentials, { api_secret: xiaomiSecret, public_key_pem: pem, test_account_json: testAccount });
  await noPersistence('synthetic-test-password-C9');
  await open('xiaomi');
  const replacementSecret = 'replacement value';
  await page.locator('#credential-api_secret').fill(replacementSecret);
  await page.locator('#credential-mode-public_key_pem').selectOption('file');
  await page.locator('#credential-file-public_key_pem').fill('/private/synthetic/public.pem');
  await page.getByRole('button', { name: '验证并保存', exact: true }).click(); await saved('xiaomi');
  assert.deepEqual(updates.at(-1).credentials, { api_secret: replacementSecret });
  assert.deepEqual(updates.at(-1).files, { public_key_pem: '/private/synthetic/public.pem' });

  await open('honor'); await page.locator('#credential-client_secret').fill('synthetic-cancel-E9');
  const canceled = await page.locator('#credentials-form').elementHandle();
  await page.getByRole('button', { name: '取消', exact: true }).click();
  assert.equal(await empty(canceled), true); await canceled.dispose();
  await open('honor'); assert.equal(await page.locator('#credential-client_secret').inputValue(), '');
  await page.locator('#credential-client_secret').fill('synthetic-navigation-F9');
  const navigated = await page.locator('#credentials-form').elementHandle();
  await page.evaluate(() => { location.hash = '#/doctor'; });
  await page.getByRole('heading', { name: '环境检查', exact: true }).waitFor();
  assert.equal(await empty(navigated), true); await navigated.dispose();
  await page.getByText('版本与运行信息', { exact: true }).click();
  await page.getByText('0.0.0-test', { exact: true }).waitFor();
  await page.getByRole('button', { name: '修复', exact: true }).click();
  await page.getByText('环境检查通过。', { exact: true }).waitFor();
  assert.equal(repairs, 1);

  await page.goto(web.url + '/#/accounts'); await open('honor');
  const failed = 'synthetic-failed-secret-G9';
  await page.locator('#credential-client_secret').fill(failed);
  failNext = true; const before = updates.length;
  await page.locator('#credentials-form').evaluate(form => { form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); });
  await page.locator('#credential-error').waitFor({ state: 'visible' });
  assert.equal(updates.length, before + 1);
  assert.equal(await page.locator('#credential-client_secret').inputValue(), '');
  assert.equal((await page.locator('#credential-error').textContent()).includes(failed), false);
  assert.equal(await page.locator('#credential-save').isEnabled(), true);
  await noPersistence(failed);
  await page.locator('#credential-client_secret').fill('synthetic-retry-H9');
  await page.getByRole('button', { name: '验证并保存', exact: true }).click(); await saved('honor');
  await open('honor'); assert.equal(await page.locator('#credential-client_secret').inputValue(), '');
  await page.keyboard.press('Escape');
  await page.setViewportSize({ width: 390, height: 844 }); await open('xiaomi');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.keyboard.press('Escape');
  for (const channel of ['oppo', 'vivo', 'tencent']) { await open(channel); assert.equal(await page.locator('#credentials-form input[data-credential-secret]').getAttribute('type'), 'password'); await page.keyboard.press('Escape'); }
  assert.equal(uploads, 0);
  assert.deepEqual(errors, []);
});

test('Web environment checks put repair beside the affected function and separate unsupported systems from repairable failures', {
  skip: process.env.PUBLISHER_BROWSER_TESTS !== '1' ? 'set PUBLISHER_BROWSER_TESTS=1 with Playwright available' : false,
  timeout: 60000,
}, async t => {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const diagnostic = (state = 'missing') => ({ ready: ['ready', 'credentials'].includes(state), version: '0.0.0-test', runtime: process.version, platform: 'darwin', arch: 'arm64',
    checks: [
      { id: 'platform', name: 'Apple Silicon Mac', status: state === 'unsupported' ? 'failed' : 'ok' },
      { id: 'apk_checker', name: '工具自带的 APK 校验组件', status: ['ready', 'credentials'].includes(state) ? 'ok' : 'missing' },
      { id: 'credentials', name: 'macOS 钥匙串', status: state === 'credentials' ? 'failed' : 'ok' },
    ] });
  let current = diagnostic(), repairs = 0, failRepair = true, releaseRepair;
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-environment-browser-'));
  const web = await startWebServer({ async invoke(action) {
    if (action === 'apps.list') return { apps: [] };
    if (action === 'accounts.list') return { accounts: [] };
    if (action === 'doctor') return current;
    if (action === 'doctor.repair') {
      repairs++;
      if (failRepair) { await new Promise(resolve => { releaseRepair = resolve; }); throw new Error('synthetic repair failure'); }
      current = diagnostic('ready'); return current;
    }
    throw new Error('unexpected synthetic action');
  } }, { root });
  let browser;
  t.after(async () => { releaseRepair?.(); await browser?.close(); await web.close(); fs.rmSync(root, { recursive: true, force: true }); });
  browser = await chromium.launch({ headless: true, ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } }), errors = [];
  page.setDefaultTimeout(10000); page.on('pageerror', error => errors.push(error.message));
  const screenshot = async name => {
    if (process.env.PUBLISHER_BROWSER_SCREENSHOTS) {
      fs.mkdirSync(process.env.PUBLISHER_BROWSER_SCREENSHOTS, { recursive: true });
      await page.screenshot({ path: path.join(process.env.PUBLISHER_BROWSER_SCREENSHOTS, name + '.png') });
    }
  };
  await page.goto(web.url + '/#/doctor');
  const apk = page.locator('.check-list li').filter({ hasText: '安装包检查' });
  await apk.getByRole('button', { name: '修复', exact: true }).waitFor();
  assert.equal(await page.locator('.page-actions').getByRole('button', { name: /修复/ }).count(), 0);
  assert.doesNotMatch(await page.locator('#main').innerText(), /组件|SDK|JDK/);
  await screenshot('environment-needs-repair');
  await apk.getByRole('button', { name: '修复', exact: true }).click();
  await apk.getByRole('button', { name: '正在修复…', exact: true }).waitFor();
  assert.equal(await apk.getByRole('button', { name: '正在修复…', exact: true }).isDisabled(), true);
  assert.equal(await page.getByRole('button', { name: '重新检查', exact: true }).isDisabled(), true);
  await apk.getByRole('button', { name: '正在修复…', exact: true }).evaluate(button => button.click());
  while (!releaseRepair) await new Promise(resolve => setTimeout(resolve, 5));
  assert.equal(repairs, 1);
  releaseRepair();
  await page.getByRole('alert').filter({ hasText: '修复未完成' }).waitFor();
  await apk.getByRole('button', { name: '修复', exact: true }).waitFor();
  failRepair = false;
  await apk.getByRole('button', { name: '修复', exact: true }).click();
  await page.getByText('环境检查通过。', { exact: true }).waitFor();
  assert.equal(repairs, 2);
  assert.equal(await page.locator('.notice.error').count(), 0);
  assert.equal(await page.getByRole('button', { name: /修复/ }).count(), 0);
  await screenshot('environment-ready');

  current = diagnostic('unsupported');
  await page.getByRole('button', { name: '重新检查', exact: true }).click();
  await page.getByText('当前版本支持使用 Apple 芯片的 Mac。', { exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: /修复/ }).count(), 0);
  current = diagnostic('credentials');
  await page.getByRole('button', { name: '重新检查', exact: true }).click();
  await page.locator('.check-list li').filter({ hasText: '账号凭据保存' }).getByText('不可用', { exact: true }).waitFor();
  assert.equal(await page.getByText('环境检查通过。', { exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: /修复/ }).count(), 0);

  current = diagnostic();
  await page.getByRole('button', { name: '重新检查', exact: true }).click();
  await apk.getByRole('button', { name: '修复', exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await screenshot('environment-needs-repair-mobile');
  assert.deepEqual(errors, []);
});
