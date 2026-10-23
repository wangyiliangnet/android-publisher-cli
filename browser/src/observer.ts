import { chromium, type BrowserContext, type Page } from 'playwright';
import { constants } from 'node:fs';
import { lstat, open, realpath } from 'node:fs/promises';
import path from 'node:path';
import http from 'node:http';
import { availabilityLabels, digest, Failure, reviewLabels, type Request } from './contract.js';

async function privateDirectory(directory: string, root: string): Promise<void> {
  if (await realpath(directory) !== directory) throw new Failure('invalid_input');
  for (let current = directory; ; current = path.dirname(current)) {
    const info = await lstat(current);
    if (!info.isDirectory() || info.isSymbolicLink() || (info.mode & 0o777) !== 0o700) throw new Failure('invalid_input');
    if (current === root) break;
    if (current === path.dirname(current)) throw new Failure('invalid_input');
  }
}

async function field(row: ReturnType<Page['locator']>, name: string, attribute?: string): Promise<string> {
  const item = row.locator(`[data-field="${name}"]`);
  if (await item.count() !== 1 || !await item.isVisible()) throw new Failure('evidence_insufficient');
  const value = attribute ? await item.getAttribute(attribute) : await item.textContent();
  if (!value || Buffer.byteLength(value) > 2048 || value !== value.trim() || /[\u0000-\u001f\u007f]/u.test(value)) throw new Failure('evidence_insufficient');
  return value;
}
const escape = (value: string) => value.replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[ch]!);
// Redirect handling in browser route interception does not cover every HTTP
// redirect hop. Fetch this single authorized fixture document ourselves, with
// no redirect following, no credential export and a hard response size limit.
async function fixtureDocument(request: Request, signal: AbortSignal): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const req = http.get(request.url, { signal, headers: { Accept: 'text/html' }, timeout: request.timeout_ms }, response => {
      if (response.statusCode && response.statusCode >= 300 && response.statusCode < 400) { response.destroy(); reject(new Failure('blocked_navigation')); return; }
      if (response.statusCode !== 200 || response.headers['x-publisher-simulation'] !== '1') { response.destroy(); reject(new Failure('evidence_insufficient')); return; }
      let size = 0; const chunks: Buffer[] = [];
      response.on('data', (chunk: Buffer) => { size += chunk.length; if (size > 1048576) { response.destroy(); reject(new Failure('evidence_insufficient')); } else chunks.push(chunk); });
      response.on('end', () => resolve(Buffer.concat(chunks)));
      response.on('error', () => reject(new Failure('evidence_insufficient')));
    });
    req.on('timeout', () => { req.destroy(); reject(new Failure('timed_out')); });
    req.on('error', () => reject(new Failure('evidence_insufficient')));
  });
}
async function save(directory: string, name: string, data: Buffer): Promise<void> {
  const handle = await open(path.join(directory, name), constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try { await handle.writeFile(data); await handle.sync(); } finally { await handle.close(); }
}

export async function collect(request: Request, canceled: Promise<never>): Promise<Record<string, unknown>> {
  const root = path.dirname(path.dirname(request.evidence_dir));
  await privateDirectory(request.profile_dir, root); await privateDirectory(request.evidence_dir, root);
  let context: BrowserContext | undefined;
  let finished = false;
  let blocked = false;
  let mainRequestSeen = false;
  let transportError: Failure | undefined;
  const transportAbort = new AbortController();
  let deadline: NodeJS.Timeout | undefined;
  const timedOut = new Promise<never>((_, reject) => { deadline = setTimeout(() => reject(new Failure('timed_out')), request.timeout_ms); });
  const launch = chromium.launchPersistentContext(request.profile_dir, {
    channel: 'chromium', headless: true, javaScriptEnabled: false, serviceWorkers: 'block', acceptDownloads: false,
    permissions: [], viewport: { width: 960, height: 720 }, timeout: request.timeout_ms,
    args: ['--disable-background-networking', '--disable-component-update', '--disable-sync'],
  }).then(async value => { context = value; if (finished) await value.close(); return value; });
  async function run(): Promise<Record<string, unknown>> {
    try { await launch; } catch (error) {
      // The diagnostic is inspected locally only; never include it in output.
      const message = error instanceof Error ? error.message : '';
      throw new Failure(/ProcessSingleton|SingletonLock|profile.*(?:in use|already used)/i.test(message) ? 'profile_busy' : 'browser_unavailable');
    }
    if (!context || finished) throw new Failure('canceled');
    context.setDefaultTimeout(Math.min(request.timeout_ms, 5000));
    const page = context.pages()[0] ?? await context.newPage();
    await context.route('**/*', async route => {
      const req = route.request();
      if (!mainRequestSeen && req.method() === 'GET' && req.url() === request.url && req.isNavigationRequest() && req.frame() === page.mainFrame()) {
        mainRequestSeen = true;
        try { const body = await fixtureDocument(request, transportAbort.signal); await route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', headers: { 'X-Publisher-Simulation': '1', 'Cache-Control': 'no-store', 'Content-Security-Policy': "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" }, body }); }
        catch (error) { transportError = error instanceof Failure ? error : new Failure('evidence_insufficient'); await route.abort('blockedbyclient'); }
      } else { if (req.isNavigationRequest() && req.frame() === page.mainFrame()) blocked = true; await route.abort('blockedbyclient'); }
    });
    await context.routeWebSocket('**/*', socket => socket.close());
    page.on('download', download => { void download.cancel(); blocked = true; });
    let response;
    try { response = await page.goto(request.url, { waitUntil: 'domcontentloaded', timeout: request.timeout_ms }); }
    catch { throw transportError ?? new Failure(blocked ? 'blocked_navigation' : 'evidence_insufficient'); }
    if (blocked || page.url() !== request.url) throw new Failure('blocked_navigation');
    if (!response || response.status() !== 200 || await response.headerValue('x-publisher-simulation') !== '1') throw new Failure('evidence_insufficient');
    const length = await response.headerValue('content-length');
    if (length && (!/^[0-9]+$/.test(length) || BigInt(length) > 1048576n)) throw new Failure('evidence_insufficient');
    if (await page.locator('[data-testid="login-required"], [data-testid="captcha-required"]').count()) throw new Failure('auth_required');
    if (await page.locator('html[data-fixture-schema="publisher-observation-v1"]').count() !== 1) throw new Failure('evidence_insufficient');
    const main = page.locator('main');
    if (await main.count() !== 1 || !await main.isVisible()) throw new Failure('evidence_insufficient');
    const t = request.target;
    for (const [attribute, value] of Object.entries({ channel: t.channel, account: t.account, 'app-id': t.app_id, 'package-name': t.package_name, scope: t.scope })) {
      if (await main.getAttribute(`data-${attribute}`) !== value) throw new Failure('evidence_insufficient');
    }
    const rows = main.locator('[data-testid="release-row"]');
    const count = await rows.count();
    if (count < 1 || count > 64) throw new Failure('evidence_insufficient');
    const matches = [];
    for (let i = 0; i < count; i++) {
      const row = rows.nth(i);
      if (!await row.isVisible()) continue;
      if (await field(row, 'version-code') === t.version_code && await field(row, 'reference') === t.remote_ref) matches.push(row);
    }
    if (matches.length !== 1) throw new Failure('evidence_insufficient');
    const row = matches[0]!;
    const reviewText = await field(row, 'review'); const availabilityText = await field(row, 'availability');
    const review = Object.hasOwn(reviewLabels, reviewText) ? reviewLabels[reviewText] : undefined;
    const availability = Object.hasOwn(availabilityLabels, availabilityText) ? availabilityLabels[availabilityText] : undefined;
    const submittedAt = await field(row, 'submitted-at', 'datetime');
    if (!review || !availability || (availability === 'live' && review !== 'approved') || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(submittedAt) || !Number.isFinite(Date.parse(submittedAt)) || Date.parse(submittedAt) > Date.now() + 30000) throw new Failure('evidence_insufficient');
    const collectedAt = new Date().toISOString(); const url = new URL(request.url).origin + `/browser/${t.channel}`;
    const observed = { channel: t.channel, account: t.account, app_id: t.app_id, scope: t.scope, package_name: t.package_name, version_code: t.version_code, remote_ref: t.remote_ref, review_state: review, availability_state: availability, review_text: reviewText, availability_text: availabilityText, submitted_at: submittedAt };
    const proof = { schema_version: 1, mode: 'simulation', kind: 'sanitized_browser_evidence', target: t, observed, collected_at: collectedAt, url };
    const text = Buffer.from(JSON.stringify(proof) + '\n');
    // Render only validated canonical fields on an offline page. Raw portal
    // pixels can include credentials; this image is explicitly derived proof.
    const card = await context.newPage();
    await card.setContent('<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'"><style>body{font:18px sans-serif;padding:32px;color:#172033;background:#f5f7fa}dl{line-height:1.6}dt{font-weight:bold}dd{overflow-wrap:anywhere;margin-bottom:12px}</style></head><body><h1>Local simulation · sanitized evidence</h1><dl>' + Object.entries({ ...observed, collected_at: collectedAt, url }).map(([key, value]) => `<dt>${escape(key)}</dt><dd>${escape(value)}</dd>`).join('') + '</dl></body></html>');
    const png = await card.screenshot({ type: 'png', fullPage: true, animations: 'disabled', timeout: Math.min(request.timeout_ms, 5000) });
    if (blocked || text.length > 65536 || png.length > 4194304) throw new Failure('evidence_insufficient');
    await privateDirectory(request.evidence_dir, root);
    await save(request.evidence_dir, 'evidence.json', text); await save(request.evidence_dir, 'evidence.png', png);
    const directory = await open(request.evidence_dir, constants.O_RDONLY); try { await directory.sync(); } finally { await directory.close(); }
    return { schema_version: 1, mode: 'simulation', status: 'observed', observation: { scope: t.scope, adapter_binding: t.adapter_binding, version_code: t.version_code, remote_ref: t.remote_ref, review_state: review, availability_state: availability, source: 'browser_fixture', collected_at: collectedAt, submitted_at: submittedAt, url }, evidence: { text_sha256: digest(text), png_sha256: digest(png), text_bytes: text.length, png_bytes: png.length } };
  }
  try { return await Promise.race([run(), timedOut, canceled]); }
  finally {
    finished = true; transportAbort.abort(); if (deadline) clearTimeout(deadline);
    // Closing our context closes only our Chromium and releases its profile.
    if (context) await context.close().catch(() => {});
    else await launch.catch(() => {});
  }
}
