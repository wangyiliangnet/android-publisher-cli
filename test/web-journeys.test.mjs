import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { startWebServer } from '../build/web.js';

const enabled = process.env.PUBLISHER_BROWSER_TESTS === '1';
const options = { skip: enabled ? false : 'set PUBLISHER_BROWSER_TESTS=1 with Playwright available', timeout: 120000 };
const names = { huawei: '华为', honor: '荣耀', tencent: '应用宝' };
const now = () => new Date().toISOString();
const clone = value => structuredClone(value);

function model(initial = 'ready') {
  const state = { apps: [], accounts: [], releases: [], connected: new Set(), prepareInputs: [], submitInputs: [], plans: new Map(), releaseCounter: 0, planCounter: 0, checks: [], credentialInputs: [], prepareGate: null, submitGate: null, unknownResult: false, queryOffline: false };
  if (initial === 'ready') {
    state.apps.push({ schema_version: 2, project_id: 'reader', display_name: '阅读助手', package_name: 'com.example.reader', channels: [
      { channel: 'huawei', account: 'huawei-main', app_id: '10001' },
      { channel: 'tencent', account: 'tencent-main', app_id: '10002', client_id: '10003' },
    ] });
    for (const row of state.apps[0].channels) { state.accounts.push({ ...row, apps: ['reader'], saved: true }); state.connected.add('reader:' + row.channel); }
  } else state.accounts.push({ channel: 'honor', account: 'honor-shared', client_id: 'shared-client', apps: [], saved: true });
  const get = id => state.releases.find(release => release.batch_id === id);
  const summary = release => { release.total_targets = release.targets.length; release.accepted_targets = release.targets.filter(row => row.execution_phase === 'submitted').length; release.approved_targets = release.targets.filter(row => row.review_state === 'approved').length; release.live_targets = release.targets.filter(row => row.availability_state === 'live').length; release.all_accepted = release.accepted_targets === release.total_targets; release.recovery_required = release.targets.some(row => row.recovery_required); return release; };
  const backend = { async invoke(action, payload) {
    if (action === 'apps.list') return { apps: state.apps, overviews: state.apps.map(app => ({ app_id: app.project_id, channels: app.channels.map(row => ({ channel: row.channel, account: row.account, saved: state.accounts.some(account => account.channel === row.channel && account.account === row.account && account.saved), connected: state.connected.has(app.project_id + ':' + row.channel), checked_at: state.connected.has(app.project_id + ':' + row.channel) ? now() : undefined })), latest_release: [...state.releases].reverse().filter(release => release.project_id === app.project_id).sort((a, b) => Number(b.has_writes) - Number(a.has_writes))[0] })) };
    if (action === 'apps.save') {
      assert.equal(typeof payload.app.display_name, 'string');
      assert.equal(JSON.stringify(payload.app).includes('private_key'), false);
      const index = state.apps.findIndex(app => app.project_id === payload.app.project_id);
      if (index < 0) state.apps.push(clone(payload.app)); else state.apps[index] = clone(payload.app);
      for (const row of payload.app.channels) { let account = state.accounts.find(item => item.channel === row.channel && item.account === row.account); if (!account) { account = { channel: row.channel, account: row.account, client_id: row.client_id, apps: [], saved: false }; state.accounts.push(account); } if (!account.apps.includes(payload.app.project_id)) account.apps.push(payload.app.project_id); }
      return { app: payload.app };
    }
    if (action === 'accounts.list') return { accounts: state.accounts.map(row => ({ ...row, app_names: row.apps.map(id => state.apps.find(app => app.project_id === id)?.display_name || id) })) };
    if (action === 'accounts.update') { state.credentialInputs.push(clone(payload)); const account = state.accounts.find(row => row.channel === payload.channel && row.account === payload.account); account.saved = true; for (const appId of account.apps) state.connected.add(appId + ':' + account.channel); return { updated: true }; }
    if (action === 'accounts.forget') { state.accounts.find(row => row.channel === payload.channel && row.account === payload.account).saved = false; return { forgotten: true }; }
    if (action === 'apps.check') { state.checks.push(clone(payload)); for (const channel of payload.channels || state.apps.find(app => app.project_id === payload.app_id).channels.map(row => row.channel)) state.connected.add(payload.app_id + ':' + channel); return { channels: [], errors: {} }; }
    if (action === 'releases.list') return { releases: state.releases.filter(release => release.project_id === payload.app_id).reverse() };
    if (action === 'releases.prepare') {
      state.prepareInputs.push(clone(payload));
      const gate = state.prepareGate; if (gate) await gate;
      if (state.returnExisting) return { release: get(state.returnExisting), duplicate: true };
      const app = state.apps.find(item => item.project_id === payload.app_id);
      const release = summary({ batch_id: 'release-' + (++state.releaseCounter), project_id: app.project_id, package_name: app.package_name, version_name: '2.0', version_code: '200', release_notes: payload.release_notes, release_policy: 'after_approval', created_at: now(), has_writes: false, can_edit: true, manual_observations: [], targets: payload.channels.map(channel => { const row = app.channels.find(item => item.channel === channel); return { ...row, artifact: { path: payload.apks[channel], filename: path.basename(payload.apks[channel]), sha256: (channel === 'huawei' ? 'a' : 'b').repeat(64), signer_sha256: 'c'.repeat(64), size: 100 }, execution_phase: 'prepared', execution_attempted: false, review_state: 'unknown', availability_state: 'unknown', query_status: 'never', last_success_at: '', recovery_required: false, recovery: [], query_support: { review: channel === 'huawei', availability: channel === 'huawei' }, manual_observations: [] }; }) });
      state.releases.push(release); return { release, duplicate: false, ...(state.relatedRelease ? { related_release: state.relatedRelease } : {}) };
    }
    if (action === 'releases.preflight') { const release = get(payload.release_id); for (const row of release.targets) if (payload.channels.includes(row.channel) && row.execution_phase !== 'submitted' && !row.recovery_required) row.execution_phase = 'preflighted'; return { release: summary(release), errors: {} }; }
    if (action === 'releases.review') { const release = get(payload.release_id), hash = String(++state.planCounter).padStart(64, '0'); const plan = { batch_id: release.batch_id, selected_channels: payload.channels, package_name: release.package_name, version_name: release.version_name, version_code: release.version_code, signer_sha256: 'c'.repeat(64), release_notes: release.release_notes, release_policy: 'after_approval', target_accounts: release.targets, artifact_sha256: Object.fromEntries(release.targets.map(row => [row.channel, row.artifact.sha256])), approval_plan: hash }; state.plans.set(hash, { plan, attempt: payload.attempt_only === true }); return { release, plan }; }
    if (action === 'releases.submit') {
      const release = get(payload.release_id), prepared = state.plans.get(payload.approval_plan);
      assert.ok(prepared); assert.equal(prepared.attempt, payload.attempt_only === true);
      state.submitInputs.push(clone(payload));
      release.has_writes = true; release.can_edit = false;
      for (const row of release.targets) if (prepared.plan.selected_channels.includes(row.channel)) { row.execution_attempted = true; row.execution_phase = 'unknown'; row.recovery_required = true; row.last_operation = { step: 'submit', effect: 'intent', created_at: now() }; }
      summary(release);
      if (state.submitGate) await state.submitGate;
      for (const row of release.targets) if (prepared.plan.selected_channels.includes(row.channel)) { row.execution_phase = state.unknownResult ? 'unknown' : 'submitted'; row.recovery_required = state.unknownResult; row.last_operation.effect = state.unknownResult ? 'unknown' : 'confirmed_applied'; }
      return { release: summary(release), errors: state.unknownResult ? { tencent: 'result unknown' } : {} };
    }
    if (action === 'releases.get') { if (payload.refresh) (state.refreshInputs ||= []).push(clone(payload)); const release = get(payload.release_id); assert.ok(release); if (payload.refresh && state.queryOffline) for (const row of release.targets) row.query_status = 'failed'; return { release: summary(release), errors: payload.refresh && state.queryOffline ? { huawei: 'network unavailable' } : {} }; }
    if (action === 'releases.reconcile') return { release: summary(get(payload.release_id)), errors: { tencent: 'still unknown' } };
    if (action === 'releases.manual') { const release = get(payload.release_id), record = { ...payload, recorded_at: now() }; release.manual_observations.push(record); release.targets.find(row => row.channel === payload.channel).manual_observations.push(record); return { release: summary(release) }; }
    if (action === 'doctor' || action === 'doctor.repair') return { ready: true, version: 'test', checks: [] };
    if (action === 'protocols') return { channels: [] };
    throw new Error('unexpected synthetic action');
  } };
  return { state, backend, summary };
}

async function fixture(t, initial = 'ready') {
  const { chromium } = await import(process.env.PUBLISHER_PLAYWRIGHT_MODULE || 'playwright');
  const fake = model(initial), root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-journey-'));
  const web = await startWebServer(fake.backend, { root }); let browser;
  t.after(async () => { fake.state.releasePrepare?.(); fake.state.releaseSubmit?.(); fake.state.prepareGate = null; await browser?.close(); await web.close(); fs.rmSync(root, { recursive: true, force: true }); });
  browser = await chromium.launch({ headless: true, ...(process.env.PUBLISHER_BROWSER_CHANNEL ? { channel: process.env.PUBLISHER_BROWSER_CHANNEL } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1080 } }), errors = [];
  page.setDefaultTimeout(15000);
  page.on('pageerror', error => errors.push(error.message));
  return { ...fake, root, web, browser, page, errors };
}

test('Web app onboarding keeps Chinese names, interrupted progress, and reusable publishing accounts', options, async t => {
  const { state, root, web, page, errors } = await fixture(t, 'empty');
  await page.goto(web.url + '/#/apps');
  await page.getByRole('link', { name: '添加应用', exact: true }).first().click();
  await page.getByLabel('应用名称', { exact: true }).fill('阅读助手');
  await page.getByLabel('Android 包名', { exact: true }).fill('com.example.reader');
  await page.getByRole('button', { name: /下一步：选择商店/ }).click();
  await page.locator('[data-setup-select=huawei]').check();
  await page.locator('[data-setup-select=honor]').check();
  await page.getByRole('button', { name: /下一步：接入发布账号/ }).click();
  await page.locator('#channel-app_id').fill('10001');
  await page.getByRole('button', { name: '保存资料并接入华为密钥', exact: true }).click();
  assert.equal(await page.locator('#credential-mode-service_account_json').inputValue(), 'text');
  assert.equal(await page.locator('#credential-text-service_account_json').isVisible(), true);
  await page.locator('#credential-text-service_account_json').fill(JSON.stringify({ key_id: 'synthetic-id', sub_account: 'synthetic-account', private_key: 'synthetic material' }));
  await page.getByRole('button', { name: '验证并保存', exact: true }).click();
  await page.getByText('荣耀 · 2 / 2', { exact: true }).waitFor();
  assert.equal(state.apps[0].display_name, '阅读助手');
  assert.match(state.apps[0].project_id, /^app-/);
  await page.reload();
  await page.getByText('荣耀 · 2 / 2', { exact: true }).waitFor();
  await page.locator('#channel-app_id').fill('10002');
  assert.equal(await page.locator('#channel-client_id').inputValue(), 'shared-client');
  await page.getByRole('button', { name: '验证并接入荣耀', exact: true }).click();
  await page.getByRole('heading', { name: '所选渠道均已接入', exact: true }).waitFor();
  assert.equal(state.credentialInputs.length, 1);
  assert.equal(state.checks.at(-1).channels[0], 'honor');
  await page.getByRole('link', { name: '进入应用', exact: true }).click();
  await page.getByRole('heading', { name: '阅读助手', exact: true }).waitFor();
  assert.equal(fs.existsSync(path.join(root, 'web-drafts', 'app', 'new-app.json')), false);
  await page.reload();
  await page.getByRole('heading', { name: '阅读助手', exact: true }).waitFor();
  assert.equal(await page.getByText('应用接入尚未完成。', { exact: true }).count(), 0);
  assert.deepEqual(errors, []);
});

test('Web publish drafts survive navigation and recheck changed materials before one background submission', options, async t => {
  const { state, root, web, page, errors } = await fixture(t);
  await page.goto(web.url);
  await page.getByRole('heading', { name: '阅读助手', exact: true }).waitFor();
  await page.getByRole('button', { name: '发新版本', exact: false }).first().click();
  await page.locator('#apk-huawei').setInputFiles({ name: 'different-name-b.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('synthetic Huawei APK') });
  await page.locator('#apk-tencent').setInputFiles({ name: 'different-name-a.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('synthetic Tencent APK') });
  await page.getByLabel(/更新说明/).fill('第一份版本说明，修复已知问题。');
  await page.waitForFunction(() => Object.keys(state.publish.uploading).length === 0);
  assert.equal(new URL(page.url()).hash.includes('new=1'), false);
  await page.reload();
  await page.getByLabel(/更新说明/).waitFor();
  assert.equal(await page.getByLabel(/更新说明/).inputValue(), '第一份版本说明，修复已知问题。');
  await page.getByRole('link', { name: '发布账号', exact: true }).click();
  await page.getByRole('link', { name: '阅读助手', exact: true }).first().click();
  await page.getByRole('link', { name: '继续这份草稿', exact: true }).click();
  assert.equal(await page.getByLabel(/更新说明/).inputValue(), '第一份版本说明，修复已知问题。');
  await page.getByRole('button', { name: /检查材料并核对提交内容/ }).click();
  await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).waitFor();
  assert.equal(state.submitInputs.length, 0);
  await page.getByRole('button', { name: '返回修改材料', exact: true }).click();
  await page.getByLabel(/更新说明/).fill('修改后的版本说明，改善使用体验。');
  await page.getByRole('button', { name: /检查材料并核对提交内容/ }).click();
  await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).waitFor();
  assert.equal(state.prepareInputs.length, 2);
  assert.equal(state.prepareInputs[1].release_notes, '修改后的版本说明，改善使用体验。');
  assert.notEqual(state.prepareInputs[1].apks.huawei, state.prepareInputs[1].apks.tencent);
  let releaseSubmit;
  state.submitGate = new Promise(resolve => { releaseSubmit = resolve; state.releaseSubmit = resolve; });
  await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).click();
  await page.getByRole('link', { name: '看实时处理进度', exact: true }).waitFor();
  await page.getByRole('link', { name: '发布账号', exact: true }).click();
  assert.equal(state.submitInputs.length, 1);
  await page.getByRole('link', { name: '阅读助手', exact: true }).first().click();
  await page.getByRole('link', { name: '看处理进度', exact: true }).first().click();
  await page.reload();
  await page.getByRole('heading', { name: /正在向商店提交/ }).waitFor();
  assert.equal(await page.getByText('结果待确认', { exact: true }).count(), 0);
  releaseSubmit(); state.submitGate = null;
  await page.getByRole('heading', { name: /所选 2 个渠道均已提交/ }).waitFor();
  await page.getByRole('link', { name: '返回应用', exact: true }).click();
  assert.equal(fs.existsSync(path.join(root, 'web-drafts', 'release', 'reader.json')), false);
  assert.equal(state.submitInputs.length, 1);
  await page.getByText(/未提交的准备记录/).waitFor();
  assert.deepEqual(errors, []);
});

test('Web does not attach an older check to a new empty draft of the same application', options, async t => {
  const { state, web, page, errors } = await fixture(t);
  let finishPrepare;
  state.prepareGate = new Promise(resolve => { finishPrepare = resolve; state.releasePrepare = resolve; });
  t.after(() => finishPrepare());
  await page.goto(web.url + '/#/publish/reader');
  await page.locator('#apk-huawei').setInputFiles({ name: 'old-huawei.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('old synthetic Huawei APK') });
  await page.locator('#apk-tencent').setInputFiles({ name: 'old-tencent.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('old synthetic Tencent APK') });
  await page.getByLabel(/更新说明/).fill('旧草稿的更新说明。');
  await page.waitForFunction(() => Object.keys(state.publish.uploading).length === 0);
  await page.getByRole('button', { name: /检查材料并核对提交内容/ }).click();
  await page.getByRole('link', { name: '阅读助手', exact: true }).first().click();
  await page.getByRole('button', { name: /发新版本/, exact: false }).click();
  await page.getByRole('button', { name: '新建空白草稿', exact: true }).click();
  await page.getByLabel(/更新说明/).waitFor();
  const flow = await page.evaluate(() => state.publish.flowId);
  finishPrepare(); state.prepareGate = null;
  await page.waitForTimeout(1800);
  assert.equal(await page.evaluate(() => state.publish.flowId), flow);
  assert.equal(await page.getByLabel(/更新说明/).inputValue(), '');
  assert.equal(await page.evaluate(() => Object.keys(state.publish.apks).length), 0);
  assert.equal(await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).count(), 0);
  assert.deepEqual(errors, []);
});

test('Web separates an unknown submission from manual review and keeps unknown manual outcomes as work', options, async t => {
  const { state, backend, summary, web, page, errors } = await fixture(t);
  const inputs = { app_id: 'reader', channels: ['huawei', 'tencent'], apks: { huawei: '/private/synthetic/h.apk', tencent: '/private/synthetic/t.apk' }, release_notes: '合成版本说明。' };
  const { release } = await backend.invoke('releases.prepare', inputs);
  release.has_writes = true; release.can_edit = false;
  release.targets[0].execution_phase = 'submitted'; release.targets[0].review_state = 'rejected'; release.targets[0].execution_attempted = true;
  release.targets[1].execution_phase = 'unknown'; release.targets[1].execution_attempted = true; release.targets[1].recovery_required = true;
  summary(release);
  await page.goto(web.url + '/#/release/' + release.batch_id);
  await page.getByRole('heading', { name: /1 个渠道已提交/ }).waitFor();
  const unknownRow = page.locator('tbody tr').filter({ has: page.getByText('应用宝', { exact: true }) }).first();
  assert.equal(await unknownRow.getByText('先确认提交结果', { exact: true }).count(), 2);
  assert.equal(await page.getByRole('button', { name: '重试应用宝', exact: true }).count(), 0);
  await page.getByRole('button', { name: '检查提交结果', exact: true }).click();
  await page.getByText(/仍未找到足够回执/).waitFor();
  release.targets[1].execution_phase = 'submitted'; release.targets[1].recovery_required = false; summary(release);
  await page.reload();
  await page.getByRole('heading', { name: /审核未通过/ }).waitFor();
  await page.locator('tbody tr').filter({ has: page.getByText('应用宝', { exact: true }) }).getByRole('button', { name: '后台查看并记录', exact: true }).click();
  await page.getByRole('button', { name: '保存本机查看记录', exact: true }).click();
  await page.getByText('应用宝：到后台查看审核和上架结果', { exact: true }).waitFor();
  await page.locator('tbody tr').filter({ has: page.getByText('应用宝', { exact: true }) }).getByRole('button', { name: '后台查看并记录', exact: true }).click();
  await page.locator('#manual-review').selectOption('approved');
  await page.getByRole('button', { name: '保存本机查看记录', exact: true }).click();
  await page.getByText('应用宝：到后台查看上架结果', { exact: true }).waitFor();
  const tableRow = page.locator('tbody tr').filter({ has: page.getByText('应用宝', { exact: true }) });
  assert.match(await tableRow.locator('td').nth(2).textContent(), /审核已通过/);
  assert.match(await tableRow.locator('td').nth(3).textContent(), /上架还不能确认/);
  release.targets[0].review_state = 'approved'; release.targets[0].availability_state = 'live';
  await page.locator('tbody tr').filter({ has: page.getByText('应用宝', { exact: true }) }).getByRole('button', { name: '后台查看并记录', exact: true }).click();
  await page.locator('#manual-review').selectOption('approved');
  await page.locator('#manual-live').selectOption('live');
  await page.getByRole('button', { name: '保存本机查看记录', exact: true }).click();
  await page.getByRole('heading', { name: /自动确认 1，人工记录 1/ }).waitFor();
  assert.equal(release.live_targets, 1);
  assert.equal(await page.locator('.running-tasks').filter({ hasText: '查询审核与上架结果' }).count(), 0);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  assert.deepEqual(errors, []);
});

test('Web resumes a readonly material check after reloading while an older check is still running', options, async t => {
  const { state, web, page, errors } = await fixture(t);
  let finishPrepare;
  state.prepareGate = new Promise(resolve => { finishPrepare = resolve; state.releasePrepare = resolve; });
  await page.goto(web.url + '/#/publish/reader');
  await page.locator('#apk-huawei').setInputFiles({ name: 'huawei.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('synthetic Huawei APK') });
  await page.locator('#apk-tencent').setInputFiles({ name: 'tencent.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('synthetic Tencent APK') });
  await page.getByLabel(/更新说明/).fill('刷新过程中仍应保留的版本说明。');
  await page.waitForFunction(() => Object.keys(state.publish.uploading).length === 0);
  await page.getByRole('button', { name: /检查材料并核对提交内容/ }).click();
  await page.getByText('检查发布材料', { exact: true }).waitFor();
  await page.reload();
  await page.getByLabel(/更新说明/).waitFor();
  assert.equal(await page.getByLabel(/更新说明/).inputValue(), '刷新过程中仍应保留的版本说明。');
  await page.getByRole('button', { name: /检查材料并核对提交内容/ }).click();
  await page.getByRole('button', { name: '重新检查所选渠道', exact: true }).waitFor();
  finishPrepare(); state.prepareGate = null;
  await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).waitFor();
  assert.equal(state.prepareInputs.length, 2);
  assert.equal(state.submitInputs.length, 0);
  assert.deepEqual(errors, []);
});

test('Web repairs the original account and retries only a failed channel while another request stays unresolved', options, async t => {
  const { state, backend, summary, web, page, errors } = await fixture(t);
  const { release } = await backend.invoke('releases.prepare', { app_id: 'reader', channels: ['huawei', 'tencent'], apks: { huawei: '/private/synthetic/h.apk', tencent: '/private/synthetic/t.apk' }, release_notes: '原发布说明保持不变。' });
  release.has_writes = true; release.can_edit = false;
  release.targets[0].execution_phase = 'unknown'; release.targets[0].recovery_required = true; release.targets[0].execution_attempted = true;
  release.targets[1].execution_phase = 'preflighted'; release.targets[1].execution_attempted = true;
  summary(release);
  await page.goto(web.url + '/#/release/' + release.batch_id);
  await page.getByRole('button', { name: '修复发布账号', exact: true }).click();
  await page.getByRole('heading', { name: /应用宝.*更新密钥/ }).waitFor();
  await page.locator('#credential-api_secret').fill('synthetic-repair-secret');
  await page.getByRole('button', { name: '验证并保存', exact: true }).click();
  await page.getByText(/应用宝发布账号已验证/).waitFor();
  await page.getByRole('button', { name: '重试应用宝', exact: true }).click();
  await page.getByRole('button', { name: '确认重试上述渠道', exact: true }).waitFor();
  const confirmationRows = page.locator('tbody tr');
  assert.equal(await confirmationRows.count(), 1);
  assert.match(await confirmationRows.first().textContent(), /应用宝/);
  assert.equal(state.submitInputs.length, 0);
  await page.getByRole('button', { name: '确认重试上述渠道', exact: true }).click();
  await page.getByRole('heading', { name: /1 个渠道已提交，1 个提交结果待确认/ }).waitFor();
  assert.equal(state.submitInputs.length, 1);
  assert.equal(state.submitInputs[0].attempt_only, true);
  const plan = state.plans.get(state.submitInputs[0].approval_plan).plan;
  assert.deepEqual(plan.selected_channels, ['tencent']);
  assert.equal(release.targets[0].recovery_required, true);
  assert.equal(release.targets[0].execution_phase, 'unknown');
  assert.equal(release.targets[1].execution_phase, 'submitted');
  assert.equal(state.credentialInputs[0].account, 'tencent-main');
  assert.deepEqual(errors, []);
});

test('Web removes a configured channel only after an explicit confirmation and keeps prior release records', options, async t => {
  const { state, backend, web, page, errors } = await fixture(t);
  await backend.invoke('releases.prepare', { app_id: 'reader', channels: ['huawei', 'tencent'], apks: { huawei: '/private/synthetic/h.apk', tencent: '/private/synthetic/t.apk' }, release_notes: '旧记录的材料保持不变。' });
  await page.goto(web.url + '/#/setup/reader');
  await page.getByRole('button', { name: '返回选择商店', exact: true }).click();
  await page.getByText('管理已配置的渠道', { exact: true }).click();
  const dialog = new Promise(resolve => page.once('dialog', async event => { assert.match(event.message(), /只影响以后的发布，已有记录和本机密钥会保留/); await event.accept(); resolve(); }));
  await page.getByRole('button', { name: '移除应用宝渠道', exact: true }).click();
  await dialog;
  await page.getByText('渠道已移除，已有发布记录仍保留。', { exact: true }).waitFor();
  assert.deepEqual(state.apps[0].channels.map(row => row.channel), ['huawei']);
  assert.equal(state.releases[0].targets.length, 2);
  assert.equal(state.accounts.find(row => row.channel === 'tencent').saved, true);
  await page.getByText('管理已配置的渠道', { exact: true }).click();
  await page.getByRole('button', { name: '移除华为渠道', exact: true }).click();
  await page.getByText('应用至少保留一个渠道，接入其它渠道后再移除。', { exact: true }).waitFor();
  assert.equal(state.apps[0].channels.length, 1);
  assert.deepEqual(errors, []);
});

test('Web starts another application explicitly without erasing an application already saved from onboarding', options, async t => {
  const { state, web, page, errors } = await fixture(t);
  await page.goto(web.url);
  await page.getByRole('heading', { name: '阅读助手', exact: true }).waitFor();
  await page.evaluate(async app => {
    const token = document.querySelector('meta[name="publisher-token"]').content;
    const result = await fetch('/api/action', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Publisher-Token': token }, body: JSON.stringify({ action: 'drafts.save', payload: { kind: 'app', id: 'new-app', value: app } }) });
    if (!result.ok) throw new Error('synthetic draft setup failed');
  }, { ...state.apps[0], channels: [...state.apps[0].channels, { channel: 'honor', account: 'honor-new', app_id: '', client_id: '' }] });
  await page.goto(web.url + '/#/setup/new');
  await page.getByRole('button', { name: '重新添加另一个应用', exact: true }).waitFor();
  const confirmed = new Promise(resolve => page.once('dialog', async event => { assert.match(event.message(), /已经保存的应用、渠道和本机密钥会保留/); await event.accept(); resolve(); }));
  await page.getByRole('button', { name: '重新添加另一个应用', exact: true }).click();
  await confirmed;
  await page.getByLabel('应用名称', { exact: true }).waitFor();
  assert.equal(await page.getByLabel('应用名称', { exact: true }).inputValue(), '');
  await page.getByText('高级：命令行短名', { exact: true }).click();
  assert.notEqual(await page.getByLabel('命令行短名', { exact: true }).inputValue(), 'reader');
  assert.equal(state.apps.length, 1);
  assert.equal(state.apps[0].display_name, '阅读助手');
  assert.equal(state.apps[0].channels.length, 2);
  assert.equal(state.accounts.every(row => row.saved), true);
  assert.deepEqual(errors, []);
});

test('Web keeps prepared records offline, opens an identical submitted record, and explains changed content before confirmation', options, async t => {
  const { state, backend, summary, web, page, errors } = await fixture(t);
  const { release } = await backend.invoke('releases.prepare', { app_id: 'reader', channels: ['huawei', 'tencent'], apks: { huawei: '/private/synthetic/h.apk', tencent: '/private/synthetic/t.apk' }, release_notes: '与原记录一致的版本说明。' });
  await page.goto(web.url + '/#/release/' + release.batch_id);
  await page.getByRole('heading', { name: '材料已保存，尚未确认提交', exact: true }).waitFor();
  assert.equal(await page.getByRole('button', { name: '后台查看并记录', exact: true }).count(), 0);
  await page.waitForTimeout(1400);
  assert.equal(state.refreshInputs?.length || 0, 0);
  for (const row of release.targets) { row.execution_phase = 'submitted'; row.execution_attempted = true; }
  release.has_writes = true; release.can_edit = false; summary(release);
  state.returnExisting = release.batch_id;
  await page.reload();
  await page.getByRole('button', { name: '准备另一次发布', exact: true }).click();
  const fillAndCheck = async notes => {
    await page.locator('#apk-huawei').setInputFiles({ name: 'h.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('same synthetic Huawei bytes') });
    await page.locator('#apk-tencent').setInputFiles({ name: 't.apk', mimeType: 'application/octet-stream', buffer: Buffer.from('same synthetic Tencent bytes') });
    await page.getByLabel(/更新说明/).fill(notes);
    await page.waitForFunction(() => Object.keys(state.publish.uploading).length === 0);
    await page.getByRole('button', { name: /检查材料并核对提交内容/ }).click();
  };
  await fillAndCheck('与原记录一致的版本说明。');
  await page.getByText(/材料与已有发布相同，已打开原发布记录/).waitFor();
  assert.match(page.url(), new RegExp('/release/' + release.batch_id + '$'));
  assert.equal(await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).count(), 0);
  assert.equal(state.submitInputs.length, 0);
  state.returnExisting = null; state.relatedRelease = release.batch_id;
  await page.getByRole('button', { name: '准备另一次发布', exact: true }).click();
  await fillAndCheck('这次只修改更新说明，需要重新核对。');
  await page.getByRole('button', { name: '确认上传并提交审核', exact: true }).waitFor();
  await page.getByText(/安装包与已有发布相同，但更新说明、应用资料或本次渠道已有变化/).waitFor();
  assert.equal(await page.getByRole('link', { name: '查看原发布', exact: true }).getAttribute('href'), '#/release/' + release.batch_id);
  assert.equal(state.submitInputs.length, 0);
  assert.equal(release.release_notes, '与原记录一致的版本说明。');
  assert.deepEqual(errors, []);
});
