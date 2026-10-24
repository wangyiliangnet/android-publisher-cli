import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { digest } from '../build/model.js';
import { Store } from '../build/state.js';

test('a killed sender leaves a durable intent and the restarted process cannot send twice', async () => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-crash-'));
  let requests = 0;
  const server = http.createServer((_request, response) => { requests++; response.writeHead(200); response.end('{}'); });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  try {
    const store = new Store(root);
    const body = { schema_version: 2, batch_id: 'crash-batch', project_id: 'synthetic', package_name: 'com.example.app',
      version_name: '2.0', version_code: '2', signer_sha256: 'a'.repeat(64), release_notes: 'Synthetic notes',
      release_policy: 'after_approval', targets: [{ channel: 'xiaomi', account: 'synthetic-account', app_id: 'com.example.app',
        artifact: { path: path.join(root, 'fixture.apk'), filename: 'fixture.apk', sha256: 'b'.repeat(64), size: 10,
          signer_sha256: 'a'.repeat(64), package_name: 'com.example.app', version_name: '2.0', version_code: '2', abis: ['arm64-v8a'] } }] };
    store.saveBatch({ ...body, digest: digest(body), created_at: new Date().toISOString() });
    const preflight = { channel: 'xiaomi', account: 'synthetic-account', app_id: 'com.example.app',
      package_name: 'com.example.app', version_code: '2', remote_version_code: '1', inspected_at: new Date().toISOString() };
    store.savePreflight('crash-batch', { ...preflight, digest: digest(preflight) });
    store.close();

    const child = spawn(process.execPath, [path.join(import.meta.dirname, 'fixtures', 'crash-after-send.mjs'),
      root, `http://127.0.0.1:${address.port}/push`], { stdio: ['ignore', 'pipe', 'pipe'] });
    const exit = await new Promise(resolve => child.on('exit', (code, signal) => resolve({ code, signal })));
    assert.equal(exit.signal, 'SIGKILL');
    assert.equal(requests, 1);

    const reopened = new Store(root);
    assert.equal(reopened.latestOperation('crash-batch', 'xiaomi', 'push').effect, 'intent');
    assert.throws(() => reopened.beginOperation('crash-batch', 'xiaomi', 'push', 'd'.repeat(64)), /already attempted/);
    assert.equal(requests, 1);
    reopened.close();
  } finally {
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(root, { recursive: true, force: true });
  }
});
