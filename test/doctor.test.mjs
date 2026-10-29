import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { AndroidTools } from '../build/artifact.js';
import { diagnose, doctorText } from '../build/doctor.js';

const cli = path.resolve('build/cli.js');
function fixture(t, version = 'latest') {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher doctor '));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const sdk = path.join(root, 'sdk');
  const java = path.join(root, 'jdk');
  const executable = (file, body = 'exit 0') => {
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, `#!/bin/sh\n${body}\n`, { mode: 0o700 });
    return file;
  };
  executable(path.join(java, 'bin/java'));
  executable(path.join(sdk, 'build-tools/36.0.0/apksigner'), 'test "$1" = version && test -x "$JAVA_HOME/bin/java"');
  executable(path.join(sdk, 'build-tools/36.0.0/aapt2'), 'test "$1" = version');
  const analyzer = executable(path.join(sdk, `cmdline-tools/${version}/bin/apkanalyzer`), 'test "$1" = --help && test -x "$JAVA_HOME/bin/java"');
  const jar = path.join(sdk, 'platforms/android-36/android.jar');
  fs.mkdirSync(path.dirname(jar), { recursive: true });
  fs.writeFileSync(jar, 'synthetic platform file');
  const args = ['doctor', '--sdk-root', sdk, '--java-home', java];
  const run = (...extra) => spawnSync(process.execPath, [cli, ...args, ...extra], { encoding: 'utf8', timeout: 10000,
    env: { ...process.env, JAVA_HOME: path.join(root, 'wrong-env-java') } });
  return { sdk, java, analyzer, run };
}

test('doctor explains how to install the missing apkanalyzer and exits 2', t => {
  const { sdk, java, analyzer, run } = fixture(t);
  fs.unlinkSync(analyzer);
  const result = run();
  assert.equal(result.status, 2);
  assert.match(result.stdout, /\[缺失\].*apkanalyzer/);
  assert.match(result.stdout, /SDK Tools.*Android SDK Command-line Tools \(latest\)/);
  assert.match(result.stdout, /Android SDK Location/);
  assert.doesNotMatch(result.stdout, /\[缺失\].*apksigner/);
  const json = JSON.parse(run('--json').stdout);
  assert.equal(json.ready, false);
  assert.equal(json.sdk_root, sdk);
  assert.equal(json.java_home, java);
  assert.equal(json.apkanalyzer, '');
  assert.equal(json.checks.find(check => check.id === 'apkanalyzer').status, 'missing');
});

test('doctor starts tools in a versioned SDK path and supports JSON for scripts', t => {
  const { run } = fixture(t, '22.0');
  const result = run('--json');
  assert.equal(result.status, 0, result.stderr);
  const json = JSON.parse(result.stdout);
  assert.equal(json.ready, true);
  assert.equal(json.schema_version, 2);
  assert.ok(json.checks.every(check => check.status === 'ok'));
  assert.match(json.apkanalyzer, /cmdline-tools\/22\.0\/bin\/apkanalyzer$/);
  assert.match(run().stdout, /环境检查通过/);
});

test('a Java launch failure blocks Java tools even when every file exists', t => {
  const { sdk, java } = fixture(t);
  fs.writeFileSync(path.join(java, 'bin/java'), '#!/bin/sh\necho synthetic-private-output >&2\nexit 1\n');
  const result = diagnose(new AndroidTools(sdk, java));
  assert.equal(result.ready, false);
  assert.equal(result.checks.find(check => check.id === 'java').status, 'failed');
  assert.equal(result.checks.find(check => check.id === 'apkanalyzer').status, 'blocked');
  assert.match(doctorText(result), /JAVA_HOME/);
  assert.doesNotMatch(JSON.stringify(result), /synthetic-private-output/);
});

test('doctor rejects non-executable tools and failing tool startup', t => {
  const { sdk, java, analyzer } = fixture(t);
  fs.chmodSync(analyzer, 0o600);
  let result = diagnose(new AndroidTools(sdk, java));
  assert.equal(result.ready, false);
  assert.match(result.checks.find(check => check.id === 'apkanalyzer').message, /执行权限/);
  fs.chmodSync(analyzer, 0o700);
  fs.writeFileSync(analyzer, '#!/bin/sh\necho synthetic-private-output >&2\nexit 9\n');
  result = diagnose(new AndroidTools(sdk, java));
  assert.equal(result.ready, false);
  assert.equal(result.checks.find(check => check.id === 'apkanalyzer').status, 'failed');
  assert.match(result.checks.find(check => check.id === 'apkanalyzer').fixes.join('\n'), /JDK/);
  assert.doesNotMatch(doctorText(result), /synthetic-private-output/);
});

test('bad SDK directories and missing platform files produce actionable diagnostics', t => {
  const { sdk, java } = fixture(t);
  fs.rmSync(path.join(sdk, 'platforms'), { recursive: true });
  let result = diagnose(new AndroidTools(sdk, java));
  assert.equal(result.ready, false);
  assert.match(result.checks.find(check => check.id === 'android_jar').fixes.join('\n'), /SDK Platforms/);
  const notDirectory = path.join(sdk, 'not-directory');
  fs.writeFileSync(notDirectory, 'synthetic');
  result = diagnose(new AndroidTools(notDirectory, java));
  assert.equal(result.ready, false);
  assert.equal(result.checks.find(check => check.id === 'sdk_root').status, 'missing');
  assert.equal(result.checks.find(check => check.id === 'apksigner').status, 'blocked');
});
