import test from 'node:test';
import assert from 'node:assert/strict';
import { configureProject } from '../build/setup.js';

function prompts(answers) {
  const labels = [];
  const messages = [];
  const queue = [...answers];
  return { labels, messages, remaining: () => queue.length,
    io: { ask: async (label, fallback) => {
      labels.push({ label, fallback });
      assert.ok(queue.length, `Unexpected question: ${label}`);
      return queue.shift();
    }, write: message => messages.push(message) } };
}

test('Huawei setup explains identifiers and automatically selects the Chinese description', async () => {
  const prompt = prompts(['huawei', '', '123456', 'yes']);
  const project = await configureProject('example', prompt.io);
  assert.deepEqual(project.channels, [{ channel: 'huawei', account: 'huawei-main', app_id: '123456' }]);
  assert.equal(prompt.remaining(), 0);
  assert.ok(prompt.labels.every(({ label }) => !label.includes('语言')));
  assert.match(prompt.messages.join(''), /账号备注由你命名/);
  assert.match(prompt.messages.join(''), /华为：App ID 123456/);
  assert.doesNotMatch(prompt.messages.join(''), /schema_version/);
});

test('Xiaomi, OPPO and vivo share a single package question and keep separate API accounts', async () => {
  const prompt = prompts(['xiaomi,oppo,vivo', 'com.example.app', '', 'example-user', '/example/icon.png',
    'https://example.invalid/privacy', 'example-oppo-client', '', 'example-vivo-key', 'yes']);
  const project = await configureProject('example', prompt.io);
  assert.equal(prompt.remaining(), 0);
  assert.equal(prompt.labels.filter(({ label }) => label.startsWith('应用包名')).length, 1);
  assert.ok(project.channels.every(row => row.app_id === 'com.example.app'));
  assert.equal(project.channels[0].client_id, 'example-user');
  assert.equal(project.channels[1].account, 'example-oppo-client');
  assert.equal(project.channels[2].client_id, 'example-vivo-key');
  assert.ok(prompt.labels.every(({ label }) => !/^(?:client_secret|access_secret|api_secret|密钥)/.test(label)));
});

test('reconfiguration retains existing account names and explicit language settings', async () => {
  const previous = { schema_version: 2, project_id: 'example', channels: [
    { channel: 'honor', account: 'existing-account', app_id: '123', client_id: 'example-client', language_id: 'zh_CN' },
  ] };
  const prompt = prompts(['', '', '', '', 'yes']);
  const project = await configureProject('example', prompt.io, previous);
  assert.deepEqual(project, previous);
  assert.match(prompt.messages.join(''), /保留已有更新说明语言设置：zh_CN/);
});

test('invalid channel and package inputs can be corrected without restarting setup', async () => {
  const prompt = prompts(['', 'unknown', 'oppo,oppo', 'oppo', 'bad package', 'com.example.app', '', 'example-client', 'yes']);
  const project = await configureProject('example', prompt.io);
  assert.equal(project.channels[0].account, 'example-client');
  assert.match(prompt.messages.join(''), /名称不能重复/);
  assert.match(prompt.messages.join(''), /请输入 APK 中的包名/);
});

test('cancel and unconfirmed summaries never return an application configuration to save', async () => {
  for (const answers of [['cancel'], ['huawei', '', '123', 'no']]) {
    await assert.rejects(configureProject('example', prompts(answers).io), /已取消配置，未保存应用配置/);
  }
});
