import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { AndroidTools, inspectAndFreeze, verifyFrozen } from '../build/artifact.js';
import { apkVersionName } from '../build/apk-metadata.js';
import { ManagedAndroidTools } from '../build/runtime.js';
import { VERIFIER_CLASS } from '../build/verifier-bytecode.js';

const tools = new AndroidTools();
const available = tools.diagnostic().ready && fs.existsSync(path.join(tools.javaHome, 'bin', 'keytool'));

test('a real signed APK is inspected and frozen; an unsigned APK is rejected',
  { skip: !available && process.env.PUBLISHER_REQUIRE_ANDROID_TESTS !== '1' }, () => {
  assert.equal(available, true, 'Android SDK and JDK are required for this test run');
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-apk-'));
  const run = (command, args) => {
    const result = spawnSync(command, args, { encoding: 'utf8', timeout: 30000,
      env: { ...process.env, JAVA_HOME: tools.javaHome } });
    assert.equal(result.status, 0, `${path.basename(command)} failed: ${result.stderr?.slice(0, 300)}`);
  };
  try {
    const manifest = path.join(root, 'AndroidManifest.xml');
    fs.writeFileSync(manifest, '<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.publisher.fixture" android:versionCode="7" android:versionName="7.0"><uses-sdk android:minSdkVersion="28" android:targetSdkVersion="36"/><application android:label="Publisher Test" android:debuggable="false"/></manifest>');
    const unsigned = path.join(root, 'unsigned.apk');
    run(tools.aapt2, ['link', '--manifest', manifest, '-I', tools.androidJar, '-o', unsigned]);
    const password = path.join(root, 'password');
    fs.writeFileSync(password, 'synthetic-fixture-only\n', { mode: 0o600 });
    const key = path.join(root, 'fixture.p12');
    run(path.join(tools.javaHome, 'bin', 'keytool'), ['-genkeypair', '-alias', 'fixture', '-keyalg', 'RSA', '-keysize', '2048',
      '-validity', '1', '-dname', 'CN=LocalPublisherFixture', '-keystore', key, '-storetype', 'PKCS12',
      '-storepass:file', password, '-keypass:file', password]);
    const signed = path.join(root, 'signed.apk');
    run(tools.apkSigner, ['sign', '--ks', key, '--ks-pass', `file:${password}`, '--ks-key-alias', 'fixture', '--out', signed, unsigned]);
    const info = tools.inspect(signed);
    assert.equal(info.package_name, 'com.example.publisher.fixture');
    assert.equal(info.version_code, '7');
    assert.equal(info.version_name, '7.0');
    assert.equal(info.signer_sha256.length, 64);
    assert.equal(apkVersionName(signed), '7.0');
    fs.writeFileSync(path.join(root, 'ApkVerifierMain.class'), Buffer.from(VERIFIER_CLASS, 'base64'));
    const standalone = spawnSync(path.join(tools.javaHome, 'bin/java'), ['-cp',
      `${path.join(path.dirname(tools.apkSigner), 'lib', 'apksigner.jar')}${path.delimiter}${root}`, 'ApkVerifierMain', signed], { encoding: 'utf8', timeout: 30000 });
    assert.equal(standalone.status, 0, 'standalone official verifier failed');
    assert.match(standalone.stdout, /^[a-f0-9]{64}\tcom\.example\.publisher\.fixture\t7\tfalse\s*$/);
    const managedRoot = process.env.PUBLISHER_MANAGED_TEST_ROOT;
    if (managedRoot) {
      const previous = Object.fromEntries(['JAVA_HOME', 'ANDROID_SDK_ROOT', 'ANDROID_HOME'].map(key => [key, process.env[key]]));
      for (const key of Object.keys(previous)) delete process.env[key];
      try {
        const managed = new ManagedAndroidTools(managedRoot);
        assert.deepEqual(managed.inspect(signed), info);
        assert.throws(() => managed.inspect(unsigned), /签名验证失败/);
      } finally {
        for (const [key, value] of Object.entries(previous)) if (value === undefined) delete process.env[key]; else process.env[key] = value;
      }
    }
    assert.throws(() => tools.inspect(unsigned));
    const resources = path.join(root, 'res', 'values');
    fs.mkdirSync(resources, { recursive: true });
    fs.writeFileSync(path.join(resources, 'strings.xml'), '<resources><string name="release_version">7.0 resource</string></resources>');
    const compiled = path.join(root, 'compiled.zip');
    run(tools.aapt2, ['compile', '--dir', path.join(root, 'res'), '-o', compiled]);
    fs.writeFileSync(manifest, fs.readFileSync(manifest, 'utf8').replace('android:versionName="7.0"', 'android:versionName="@string/release_version"'));
    const resourceApk = path.join(root, 'resource.apk');
    run(tools.aapt2, ['link', '--manifest', manifest, '-I', tools.androidJar, '-o', resourceApk, compiled]);
    assert.equal(apkVersionName(resourceApk), '7.0 resource');
    const frozen = inspectAndFreeze(tools, signed, root);
    verifyFrozen(tools, frozen);
    fs.appendFileSync(frozen.path, 'tampered');
    assert.throws(() => verifyFrozen(tools, frozen), /differs/);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
