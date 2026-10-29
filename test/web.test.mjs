import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { startWebServer } from '../build/web.js';
import { PublisherApplication } from '../build/application.js';

async function fixture(t, invoke = async () => ({ apps: [] })) {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-web-'));
  const app = await startWebServer({ invoke }, { root });
  t.after(async () => { await app.close(); fs.rmSync(root, { recursive: true, force: true }); });
  const response = await fetch(app.url);
  const html = await response.text();
  const token = html.match(/name="publisher-token" content="([^"]+)"/)?.[1];
  assert.ok(token);
  return { ...app, root, token, html, response,
    action: (action, payload = {}, extra = {}) => fetch(app.url + '/api/action', {
      method: 'POST', headers: { 'Content-Type': 'application/json', Origin: app.url, 'X-Publisher-Token': token, ...extra },
      body: JSON.stringify({ action, payload }),
    }),
    upload: (filename, content) => fetch(app.url + '/api/upload', {
      method: 'POST', headers: { 'Content-Type': 'application/octet-stream', Origin: app.url, 'X-Publisher-Token': token, 'X-Filename': encodeURIComponent(filename) },
      body: content,
    }),
  };
}

function rawRequest(url, headers, body = '') {
  return new Promise((resolve, reject) => {
    const target = new URL(url);
    const request = http.request({ hostname: target.hostname, port: target.port, path: target.pathname,
      method: 'POST', headers, agent: false }, response => {
      let text = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { text += chunk; });
      response.on('end', () => resolve({ status: response.statusCode, body: text }));
    });
    request.on('error', reject); request.end(body);
  });
}

test('local web serves self-contained assets on loopback with security headers', async t => {
  const app = await fixture(t);
  assert.equal(new URL(app.url).hostname, '127.0.0.1');
  assert.equal(app.response.headers.get('x-frame-options'), 'DENY');
  assert.equal(app.response.headers.get('x-content-type-options'), 'nosniff');
  assert.match(app.response.headers.get('content-security-policy'), /frame-ancestors 'none'/);
  assert.match(app.html, /lang="zh-CN"/);
  const script = await fetch(app.url + '/app.js');
  assert.match(script.headers.get('content-type'), /javascript/);
  const source = await script.text();
  assert.doesNotThrow(() => new Function(source));
  assert.equal((await fetch(app.url + '/app.css')).status, 200);
  assert.equal((await fetch(app.url + '/does-not-exist')).status, 404);
});

test('API requires matching origin, host and session token before dispatch', async t => {
  let calls = 0;
  const app = await fixture(t, async () => { calls++; return { apps: [] }; });
  assert.equal((await app.action('apps.list', {}, { Origin: 'https://example.invalid' })).status, 403);
  assert.equal((await app.action('apps.list', {}, { 'X-Publisher-Token': 'wrong' })).status, 403);
  const missingOrigin = await fetch(app.url + '/api/action', { method: 'POST', headers: {
    'Content-Type': 'application/json', 'X-Publisher-Token': app.token,
  }, body: JSON.stringify({ action: 'apps.list', payload: {} }) });
  assert.equal(missingOrigin.status, 403);
  const rebound = await rawRequest(app.url + '/api/action', { Host: 'example.invalid', Origin: app.url,
    'X-Publisher-Token': app.token, 'Content-Type': 'application/json' }, '{}');
  assert.equal(rebound.status, 403);
  assert.equal(calls, 0);
  assert.equal((await app.action('apps.list')).status, 200);
  assert.equal(calls, 1);
});

test('API validates action envelope and rejects duplicate JSON identity fields', async t => {
  let calls = 0;
  const app = await fixture(t, async () => { calls++; return {}; });
  assert.equal((await app.action('unexpected.action')).status, 400);
  const duplicate = await fetch(app.url + '/api/action', { method: 'POST', headers: {
    Origin: app.url, 'Content-Type': 'application/json', 'X-Publisher-Token': app.token,
  }, body: '{"action":"apps.list","action":"releases.submit","payload":{}}' });
  assert.equal(duplicate.status, 400);
  assert.equal(calls, 0);
});

test('backend actions are serialized and receive the exact explicit channel mapping', async t => {
  const events = [];
  let finishFirst;
  const pending = new Promise(resolve => { finishFirst = resolve; });
  const app = await fixture(t, async (action, payload) => {
    events.push({ action, payload, phase: 'start' });
    if (action === 'releases.prepare') await pending;
    events.push({ action, phase: 'finish' });
    return { ok: true };
  });
  const payload = { app_id: 'demo', apks: { honor: '/private/one.apk', huawei: '/private/two.apk' }, release_notes: 'Synthetic release notes' };
  const first = app.action('releases.prepare', payload);
  while (!events.length) await new Promise(resolve => setTimeout(resolve, 5));
  const second = app.action('releases.list', { app_id: 'demo' });
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(events.length, 1);
  finishFirst();
  await Promise.all([first, second]);
  assert.deepEqual(events[0].payload, payload);
  assert.deepEqual(events.map(event => event.action + ':' + event.phase), [
    'releases.prepare:start', 'releases.prepare:finish', 'releases.list:start', 'releases.list:finish',
  ]);
});

test('uploads use random private directories and preserve explicit filenames only as labels', async t => {
  const app = await fixture(t);
  const response = await app.upload('渠道包.apk', Buffer.from('synthetic APK'));
  assert.equal(response.status, 201);
  const uploaded = await response.json();
  assert.equal(uploaded.filename, '渠道包.apk');
  assert.ok(uploaded.path.startsWith(path.join(app.root, 'web-uploads') + path.sep));
  assert.equal(fs.statSync(uploaded.path).mode & 0o777, 0o600);
  assert.equal(fs.statSync(path.dirname(uploaded.path)).mode & 0o777, 0o700);
  assert.equal(fs.readFileSync(uploaded.path, 'utf8'), 'synthetic APK');
  const next = await (await app.upload('渠道包.apk', Buffer.from('another synthetic APK'))).json();
  assert.notEqual(next.path, uploaded.path);
  await app.close();
  assert.equal(fs.existsSync(uploaded.path), false);
});

test('uploads reject traversal, empty files, oversized declarations and unauthenticated writes', async t => {
  const app = await fixture(t);
  assert.equal((await app.upload('../app.apk', 'synthetic')).status, 400);
  assert.equal((await app.upload('app.apk', '')).status, 400);
  const unauthenticated = await fetch(app.url + '/api/upload', { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: 'synthetic' });
  assert.equal(unauthenticated.status, 403);
  const oversized = await rawRequest(app.url + '/api/upload', { Host: new URL(app.url).host,
    Origin: app.url, 'X-Publisher-Token': app.token, 'Content-Type': 'application/octet-stream',
    'X-Filename': 'app.apk', 'Content-Length': String(1024 * 1024 * 1024 + 1) });
  assert.equal(oversized.status, 413);
});

test('explicit app icons use private durable storage so saved configuration survives shutdown', async t => {
  const app = await fixture(t);
  const send = filename => fetch(app.url + '/api/upload', { method: 'POST', headers: {
    'Content-Type': 'application/octet-stream', Origin: app.url, 'X-Publisher-Token': app.token,
    'X-Filename': encodeURIComponent(filename), 'X-Upload-Purpose': 'app-icon',
  }, body: Buffer.from('synthetic icon data') });
  assert.equal((await send('app.apk')).status, 400);
  const response = await send('app.png');
  assert.equal(response.status, 201);
  const saved = await response.json();
  assert.ok(saved.path.startsWith(path.join(app.root, 'web-assets', 'icons') + path.sep));
  assert.equal(fs.statSync(saved.path).mode & 0o777, 0o600);
  await app.close();
  assert.equal(fs.readFileSync(saved.path, 'utf8'), 'synthetic icon data');
});

test('arbitrary backend errors never expose secret material to the browser', async t => {
  const app = await fixture(t, async () => { throw new Error('synthetic-secret-token=https://example.invalid/signed?secret=synthetic'); });
  const response = await app.action('releases.preflight', { release_id: 'demo-1' });
  assert.equal(response.status, 400);
  const body = await response.text();
  assert.doesNotMatch(body, /synthetic-secret|signed\?/);
  assert.match(body, /预检未完成/);
});

test('Web uses the real shared application for APK-free setup and missing-credential guidance', async t => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-web-app-'));
  let remoteCalls = 0;
  const publisher = new PublisherApplication({ root, fetcher: async () => { remoteCalls++; throw new Error('unexpected remote request'); } });
  const web = await startWebServer(publisher, { root });
  t.after(async () => { await web.close(); publisher.close(); fs.rmSync(root, { recursive: true, force: true }); });
  const html = await (await fetch(web.url)).text();
  const token = html.match(/name="publisher-token" content="([^"]+)"/)[1];
  const action = async (action, payload = {}) => {
    const response = await fetch(web.url + '/api/action', { method: 'POST', headers: {
      Origin: web.url, 'Content-Type': 'application/json', 'X-Publisher-Token': token,
    }, body: JSON.stringify({ action, payload }) });
    assert.equal(response.status, 200);
    return response.json();
  };
  assert.deepEqual(await action('apps.list'), { apps: [] });
  const app = { schema_version: 2, project_id: 'synthetic-app', package_name: 'com.example.app', channels: [
    { channel: 'huawei', account: 'huawei-main', app_id: '10001' },
    { channel: 'honor', account: 'honor-main', app_id: '10002', client_id: '10003' },
  ] };
  assert.deepEqual(await action('apps.save', { app }), { app });
  assert.deepEqual(publisher.apps(), [app]);
  assert.deepEqual(await action('releases.list', { app_id: app.project_id }), { releases: [] });
  const accounts = await action('accounts.list');
  assert.equal(accounts.accounts.length, 2);
  assert.ok(accounts.accounts.every(row => row.saved === false));
  const checks = await action('apps.check', { app_id: app.project_id });
  assert.equal(Object.keys(checks.errors).length, 2);
  assert.match(checks.errors.huawei, /account update/);
  assert.equal(remoteCalls, 0);
});

test('credential update is protected, strips backend output and clears request material', async t => {
  let captured, received;
  const app=await fixture(t,async(action,payload)=>{
    assert.equal(action,'accounts.update');captured=payload;received=structuredClone(payload);
    return {updated:true,credentials:payload.credentials,extra:'synthetic-private-output'};
  });
  const payload={channel:'xiaomi',account:'xiaomi-main',credentials:{api_secret:'synthetic'},files:{public_key_pem:'/synthetic/material.pem'}};
  assert.equal((await app.action('accounts.update',payload,{Origin:'https://example.invalid'})).status,403);
  assert.equal(captured,undefined);
  const response=await app.action('accounts.update',payload);
  assert.equal(response.status,200);assert.equal(response.headers.get('cache-control'),'no-store');
  assert.deepEqual(await response.json(),{updated:true});assert.deepEqual(received,payload);
  assert.equal(captured.credentials,undefined);assert.equal(captured.files,undefined);
});

test('credential update failures expose only fixed input or verification errors', async t=>{
  const {CredentialInputError}=await import('../build/credentials.js');
  let retained;
  const app=await fixture(t,async(_action,payload)=>{retained=payload;throw new Error(JSON.stringify(payload));});
  const response=await app.action('accounts.update',{channel:'honor',account:'honor-main',credentials:{client_secret:'synthetic-private-input'}});
  const text=await response.text();assert.equal(response.status,400);assert.doesNotMatch(text,/synthetic-private-input|client_secret/);
  assert.equal(retained.credentials,undefined);
  const invalid=await fixture(t,async()=>{throw new CredentialInputError('file');});
  const bad=await invalid.action('accounts.update',{});assert.match((await bad.json()).error,/0600/);
});

test('component repair is available through the protected action route',async t=>{
  const calls=[];const app=await fixture(t,async(action,payload)=>{calls.push({action,payload});return {ready:true,checks:[]};});
  const response=await app.action('doctor.repair');assert.equal(response.status,200);
  assert.deepEqual(await response.json(),{ready:true,checks:[]});assert.deepEqual(calls,[{action:'doctor.repair',payload:{}}]);
});

test('Web account onboarding uses the shared validator, retains previous credentials on rejection and writes ciphertext only',async t=>{
  const {CredentialVault}=await import('../build/vault.js');
  const root=fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()),'publisher-web-credentials-'));
  const keys=new Map();
  const keyStore={get:id=>keys.has(id)?Buffer.from(keys.get(id)):undefined,put:(id,key)=>keys.set(id,Buffer.from(key)),delete:id=>keys.delete(id)};
  const vault=new CredentialVault(root,keyStore);let reject=false;const calls=[];
  const publisher=new PublisherApplication({root,vault,fetcher:async(url,init)=>{
    const pathname=new URL(url).pathname;calls.push({method:init.method,path:pathname});
    assert.ok(pathname.endsWith('query_app_detail'),'only the readonly connection check is allowed');
    if(reject)throw new Error('synthetic-private-input');
    return Response.json({ret:0,pkg_name:'com.example.app',version_code:1,app_name:'Synthetic',introduce:'Synthetic'});
  }});
  publisher.saveApp({schema_version:2,project_id:'synthetic',package_name:'com.example.app',channels:[{channel:'tencent',account:'tencent-main',app_id:'456',client_id:'synthetic-user'}]});
  const web=await startWebServer(publisher,{root});
  t.after(async()=>{await web.close();publisher.close();fs.rmSync(root,{recursive:true,force:true});});
  const html=await(await fetch(web.url)).text(),token=html.match(/name="publisher-token" content="([^"]+)"/)[1];
  const action=(action,payload={})=>fetch(web.url+'/api/action',{method:'POST',headers:{Origin:web.url,'Content-Type':'application/json','X-Publisher-Token':token},body:JSON.stringify({action,payload})});
  const body={channel:'tencent',account:'tencent-main',credentials:{api_secret:'synthetic-private-input'}};
  let response=await action('accounts.update',body);assert.equal(response.status,200);assert.deepEqual(await response.json(),{updated:true});
  assert.equal(vault.get('tencent','tencent-main').api_secret,'synthetic-private-input');
  const listing=await(await action('accounts.list')).json();assert.equal(listing.accounts[0].saved,true);
  assert.ok(!JSON.stringify(listing).includes('synthetic-private-input'));
  for(const filename of fs.readdirSync(path.join(root,'credentials'))){const file=path.join(root,'credentials',filename);assert.equal(fs.statSync(file).mode&0o777,0o600);assert.ok(!fs.readFileSync(file,'utf8').includes('synthetic-private-input'));}
  reject=true;response=await action('accounts.update',{...body,credentials:{api_secret:'synthetic-replacement'}});
  assert.equal(response.status,400);assert.doesNotMatch(await response.text(),/synthetic-private-input|synthetic-replacement/);
  assert.equal(vault.get('tencent','tencent-main').api_secret,'synthetic-private-input');
  assert.equal(publisher.store.batchIds().length,0);assert.equal(calls.length,2);
  response=await action('accounts.update',{...body,credentials:{api_secret:'synthetic'},files:{service_account_json:'/synthetic.json'}});
  assert.equal(response.status,400);assert.equal(calls.length,2);
});
