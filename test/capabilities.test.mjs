import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { CHANNELS } from '../build/model.js';
import { protocols, validateReleaseNotes } from '../build/channels/capabilities.js';

test('capabilities disclose query limits separately from real account verification', () => {
  const channels = protocols();
  assert.deepEqual(channels.map(item => item.channel), [...CHANNELS]);
  for (const item of channels) {
    assert.equal(item.protocol_mock_verified, true);
    assert.equal(item.account_verified, false);
    assert.equal(item.live_verified, false);
    assert.equal(item.write_reconciliation, false);
  }
  for (const name of ['xiaomi', 'tencent']) {
    const item = channels.find(item => item.channel === name);
    assert.equal(item.review_query, false);
    assert.equal(item.availability_query, false);
  }
  assert.equal(channels.find(item => item.channel === 'honor').availability_query, false);
  assert.equal(channels.find(item => item.channel === 'vivo').release_notes_max, 200);
});

test('protocols is readable offline and offers explicit JSON output', () => {
  const run = args => spawnSync(process.execPath, ['build/cli.js', 'protocols', ...args], { encoding: 'utf8' });
  const human = run([]);
  assert.equal(human.status, 0);
  assert.match(human.stdout, /尚未完成真实账号/);
  assert.match(human.stdout, /后台核对/);
  assert.match(human.stdout, /不能自动核对响应丢失/);
  const machine = run(['--json']);
  assert.equal(machine.status, 0);
  assert.equal(JSON.parse(machine.stdout).channels.length, 6);
});

test('release note limits count Unicode characters and include whitespace in the maximum', () => {
  assert.doesNotThrow(() => validateReleaseNotes('😀'.repeat(200), ['vivo']));
  assert.throws(() => validateReleaseNotes('😀'.repeat(201), ['vivo']), /vivo.*200.*201/);
  assert.doesNotThrow(() => validateReleaseNotes('更'.repeat(500), ['huawei', 'tencent']));
  assert.throws(() => validateReleaseNotes('更'.repeat(501), ['huawei']), /huawei.*500.*501/);
  assert.throws(() => validateReleaseNotes('更'.repeat(200) + '\n', ['vivo']), /200.*201/);
  assert.throws(() => validateReleaseNotes('    更新    ', ['vivo']), /至少/);
});
