import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readFile, rm, realpath, stat, lstat, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { chromium } from 'playwright';
import { collect } from '../dist/observer.js';
import { digest, Failure, parseRequest } from '../dist/contract.js';

function target() {
  const t = { channel: 'honor', account: 'simulation-account', app_id: 'simulation-app', package_name: 'com.example.simulation', version_code: '9223372036854775807', remote_ref: '9007199254740993-submit', adapter_binding: '', intent_digest: 'a'.repeat(64) };
  return { ...t, scope: digest(JSON.stringify([t.channel, t.account, t.app_id, t.package_name])) };
}
function markup(t, change = {}) {
  const v = { ...t, ...change };
  return `<html data-fixture-schema="publisher-observation-v1"><body><aside>secret-outside-pane</aside><main data-channel="${v.channel}" data-account="${v.account}" data-app-id="${v.app_id}" data-package-name="${v.package_name}" data-scope="${v.scope}"><article data-testid="release-row"><span data-field="version-code">${v.version_code}</span><span data-field="reference">${v.remote_ref}</span><time data-field="submitted-at" datetime="2026-01-01T00:00:00Z">2026-01-01T00:00:00Z</time><span data-field="review">${v.review ?? '审核通过'}</span><span data-field="availability">${v.availability ?? '已上线'}</span></article></main></body></html>`;
}
async function fixture(fn) {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'publisher-browser-test-')));
  const t = target();
  const profile = path.join(root, 'browser', t.channel, digest(t.account), 'profile');
  await mkdir(profile, { mode: 0o700, recursive: true });
  let handler = (_req, res) => { res.setHeader('X-Publisher-Simulation', '1'); res.end(markup(t)); };
  const server = http.createServer((req, res) => handler(req, res));
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const url = `http://127.0.0.1:${server.address().port}/browser/${t.channel}?scope=${t.scope}`;
  let serial = 1;
  async function request() {
    const evidence = path.join(root, 'evidence', (serial++).toString(16).padStart(64, '0'));
    await mkdir(evidence, { mode: 0o700, recursive: true });
    return { schema_version: 1, mode: 'simulation', target: t, url, profile_dir: profile, evidence_dir: evidence, timeout_ms: 10000 };
  }
  try { await fn({ t, root, request, handler: value => { handler = value; } }); }
  finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); await rm(root, { recursive: true, force: true }); }
}
const never = () => new Promise(() => {});

test('strict contract: exact identities, int64 strings, local origin and managed paths', async () => fixture(async ({ request }) => {
  const q = await request(); assert.deepEqual(parseRequest(JSON.stringify(q)), q);
  for (const mutate of [
    q => { q.target.version_code = 9223372036854775807; },
    q => { q.target.version_code = '9223372036854775808'; },
    q => { q.target.account = 'other-account'; },
    q => { q.target.injected = 'unknown'; },
    q => { q.mode = 'production'; },
    q => { q.url = q.url.replace('127.0.0.1', 'localhost'); },
    q => { q.url += '&token=secret'; },
    q => { q.profile_dir += '/elsewhere'; },
    q => { q.timeout_ms = 30001; },
  ]) { const bad = structuredClone(q); mutate(bad); assert.throws(() => parseRequest(JSON.stringify(bad)), /invalid_input/); }
  assert.throws(() => parseRequest(JSON.stringify(q).replace('"schema_version":1', '"schema_version":1,"schema_version":1')), /invalid_input/);
}));

test('real browser selects exact int64 target and emits private derived evidence', async () => fixture(async ({ request, handler, t }) => {
  let unsafeRequests = 0;
  handler((req, res) => {
    if (req.url.startsWith('/browser/')) { res.setHeader('X-Publisher-Simulation', '1'); res.end(markup(t).replace('</body>', `<img src="/secret"><script>fetch('/write',{method:'POST'})</script><iframe src="/other"></iframe></body>`)); }
    else { unsafeRequests++; res.end('secret'); }
  });
  const q = await request(); const result = await collect(q, never());
  assert.equal(result.status, 'observed'); assert.equal(result.observation.version_code, t.version_code);
  assert.equal(unsafeRequests, 0);
  const text = await readFile(path.join(q.evidence_dir, 'evidence.json'));
  assert.equal(digest(text), result.evidence.text_sha256); assert.equal(text.includes('secret-outside-pane'), false);
  assert.equal(text.includes('?scope='), false); assert.equal(JSON.parse(text).observed.remote_ref, t.remote_ref);
  for (const name of ['evidence.json', 'evidence.png']) assert.equal((await stat(path.join(q.evidence_dir, name))).mode & 0o777, 0o600);
}));

test('missing, old, duplicate, hidden and unknown evidence stays insufficient', async () => fixture(async ({ request, handler, t }) => {
  const valid = markup(t);
  const cases = [
    markup(t, { version_code: '2' }), markup(t, { remote_ref: 'other-ref' }), markup(t, { account: 'other-account' }),
    markup(t, { review: 'token-or-unknown' }), markup(t, { review: '审核中', availability: '已上线' }),
    valid.replace('<article ', '<article hidden '), valid.replace('<time ', '<time hidden '), valid.replace('data-field="reference"', 'data-field="other"'),
    valid.replace('</main>', valid.match(/<article.*?<\/article>/s)[0] + '</main>'),
    valid + 'x'.repeat(1048576),
  ];
  for (const html of cases) {
    handler((_req, res) => { res.setHeader('X-Publisher-Simulation', '1'); res.end(html); });
    await assert.rejects(collect(await request(), never()), /evidence_insufficient/);
  }
}));

test('redirect is blocked before contacting another local origin; auth requires human', async () => fixture(async ({ request, handler }) => {
  let reached = 0; const other = http.createServer((_req, res) => { reached++; res.end('secret'); });
  await new Promise(resolve => other.listen(0, '127.0.0.1', resolve));
  try {
    handler((_req, res) => { res.writeHead(302, { Location: `http://127.0.0.1:${other.address().port}/login?token=secret` }); res.end(); });
    await assert.rejects(collect(await request(), never()), /blocked_navigation/); assert.equal(reached, 0);
  } finally { other.closeAllConnections(); await new Promise(resolve => other.close(resolve)); }
  handler((_req, res) => { res.setHeader('X-Publisher-Simulation', '1'); res.end('<div data-testid="login-required">login</div>'); });
  await assert.rejects(collect(await request(), never()), /auth_required/);
}));

test('symlink path cannot export evidence outside managed root', async () => fixture(async ({ request, root }) => {
  const q = await request(); await rm(q.evidence_dir, { recursive: true });
  await symlink(root, q.evidence_dir);
  await assert.rejects(collect(q, never()), /invalid_input/);
}));


test('dedicated full Chromium profile occupancy is respected without deleting locks', async () => fixture(async ({ request }) => {
  const q = await request();
  const owner = await chromium.launchPersistentContext(q.profile_dir, { channel: 'chromium', headless: true });
  try {
    await assert.rejects(collect(q, never()), /profile_busy/);
    const page = owner.pages()[0]; await page.setContent('<p>owner remains alive</p>');
    assert.equal(await page.locator('p').textContent(), 'owner remains alive');
    await lstat(path.join(q.profile_dir, 'SingletonLock'));
  } finally { await owner.close(); }
  const result = await collect(await request(), never()); assert.equal(result.status, 'observed');
}));

test('cancellation during launch closes late context before profile reuse', async () => fixture(async ({ request }) => {
  const canceled = Promise.reject(new Failure('canceled')); void canceled.catch(() => {});
  await assert.rejects(collect(await request(), canceled), /canceled/);
  assert.equal((await collect(await request(), never())).status, 'observed');
}));
