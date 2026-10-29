import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { WebWorkspace } from '../build/web/workspace.js';

test('incomplete forms and explicit APK mappings survive workspace restart without storing credentials', t => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-workspace-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const workspace = new WebWorkspace(root);
  const app = { schema_version: 2, project_id: 'example', display_name: '示例阅读', package_name: '',
    channels: [{ channel: 'huawei', account: '', app_id: '123' }] };
  const release = { app_id: 'example', channels: ['huawei'], apks: { huawei: '/private/synthetic/one.apk' }, release_notes: '本次更新\n第二行' };
  workspace.save('app', 'new-app', app); workspace.save('release', 'example', release);
  const restarted = new WebWorkspace(root);
  assert.deepEqual(restarted.get('app', 'new-app'), app); assert.deepEqual(restarted.get('release', 'example'), release);
  for (const [kind, id] of [['app', 'new-app'], ['release', 'example']]) {
    assert.equal(fs.statSync(path.join(root, 'web-drafts', kind, id + '.json')).mode & 0o777, 0o600);
  }
  for (const value of [{ ...app, credentials: { client_secret: 'synthetic' } },
    { ...app, channels: [{ channel: 'huawei', client_secret: 'synthetic' }] }]) assert.throws(() => workspace.save('app', 'new-app', value));
  assert.throws(() => workspace.save('release', 'example', { ...release, approval_plan: 'a'.repeat(64) }));
  assert.throws(() => workspace.save('release', 'other', release));
  assert.throws(() => workspace.get('release', '../outside'));
  assert.throws(() => workspace.get(['app'], 'new-app'));
  assert.deepEqual(restarted.get('app', 'new-app'), app);
  workspace.clear('release', 'example'); assert.equal(workspace.get('release', 'example'), null);
});

test('workspace refuses symlinked or public draft files', t => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-workspace-private-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const workspace = new WebWorkspace(root);
  workspace.save('app', 'new-app', { display_name: '示例阅读' });
  const file = path.join(root, 'web-drafts', 'app', 'new-app.json');
  fs.chmodSync(file, 0o644); assert.throws(() => workspace.get('app', 'new-app')); fs.chmodSync(file, 0o600);
  const target = path.join(root, 'original.json'); fs.renameSync(file, target); fs.symlinkSync(target, file);
  assert.throws(() => workspace.get('app', 'new-app')); assert.throws(() => workspace.clear('app', 'new-app'));
  assert.equal(fs.existsSync(target), true);
});
