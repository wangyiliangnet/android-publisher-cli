import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { type ArtifactInspector, apkAbis } from './artifact.js';
import { apkVersionName } from './apk-metadata.js';
import { validHash, validPackage, validVersion } from './model.js';
import { dataRoot, hashFile, privateDir, readJSON, regularFile, writePrivate } from './private-file.js';
import { VERIFIER_CLASS, VERIFIER_SHA256 } from './verifier-bytecode.js';

export const RUNTIME_RELEASE = {
  directory: 'checker-1',
  java: 'jdk-21.0.13+9-jre/Contents/Home/bin/java',
  java_url: 'https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/OpenJDK21U-jre_aarch64_mac_hotspot_21.0.13_9.tar.gz',
  java_sha256: '225bab0a09491ef06835488cd942f5d2befa9a0410a2484f0c8bbcf7506ec09a',
  apksig_url: 'https://dl.google.com/dl/android/maven2/com/android/tools/build/apksig/8.13.0/apksig-8.13.0.jar',
  apksig_sha256: 'c070ed1394629d74641aa0906f60b2ffa1ee77e6366a1f93437f59717b1aeb89',
} as const;

export function runtimeDirectory(root = dataRoot()): string { return path.join(root, 'runtime', RUNTIME_RELEASE.directory); }

function verifyRuntime(root: string): { directory: string; java: string } {
  const directory = runtimeDirectory(root);
  const receipt = readJSON(path.join(directory, 'installation.json'), 4096, true) as { java_sha256: string };
  const java = path.join(directory, RUNTIME_RELEASE.java);
  if (!validHash(receipt?.java_sha256) || hashFile(java).sha256 !== receipt.java_sha256 ||
    hashFile(path.join(directory, 'apksig.jar')).sha256 !== RUNTIME_RELEASE.apksig_sha256 ||
    hashFile(path.join(directory, 'ApkVerifierMain.class')).sha256 !== VERIFIER_SHA256) throw new Error('APK 检查组件已损坏');
  return { directory, java };
}

export function runtimeDiagnostic(root = dataRoot()): Record<string, unknown> {
  const platformOK = process.platform === 'darwin' && process.arch === 'arm64';
  let installed = false;
  try { verifyRuntime(root); installed = true; } catch { /* Diagnostic remains read only. */ }
  return { schema_version: 2, ready: platformOK && installed, platform: process.platform, arch: process.arch,
    managed: true, checks: [
      { id: 'platform', status: platformOK ? 'ok' : 'failed', name: 'Apple Silicon Mac' },
      { id: 'apk_checker', status: installed ? 'ok' : 'missing', name: '工具自带的 APK 校验组件',
        fixes: installed ? [] : ['运行 android-release doctor --repair，或在首次发布时自动安装检查组件。'] },
      { id: 'credentials', status: fs.existsSync('/usr/bin/security') ? 'ok' : 'failed', name: 'macOS 钥匙串' },
    ] };
}

async function download(url: string, file: string, expected: string, max: number): Promise<void> {
  const response = await fetch(url, { signal: AbortSignal.timeout(180000), headers: { 'User-Agent': 'AndroidPublisherCLI' } });
  if (!response.ok || !response.body) throw new Error('检查组件下载失败，请检查网络后重试');
  const fd = fs.openSync(file, 'wx', 0o600);
  const hash = createHash('sha256');
  let size = 0;
  try {
    for await (const bytes of response.body) {
      size += bytes.length;
      if (size > max) throw new Error('检查组件下载大小异常');
      hash.update(bytes);
      let offset = 0;
      while (offset < bytes.length) offset += fs.writeSync(fd, bytes, offset, bytes.length - offset);
    }
    fs.fsyncSync(fd);
    if (hash.digest('hex') !== expected) throw new Error('检查组件校验值不匹配，已停止安装');
  } finally { fs.closeSync(fd); }
}

export async function ensureRuntime(root = dataRoot(), progress: (message: string) => void = () => {}): Promise<ManagedAndroidTools> {
  try { verifyRuntime(root); return new ManagedAndroidTools(root); } catch { /* Install verified components below. */ }
  if (process.platform !== 'darwin' || process.arch !== 'arm64') throw new Error('当前版本需要 Apple Silicon Mac');
  const base = path.join(root, 'runtime');
  privateDir(base);
  const lock = path.join(base, 'install.lock');
  if (fs.existsSync(lock)) {
    const pid = regularFile(lock, 64, true).toString('utf8');
    let alive = /^[1-9][0-9]*$/.test(pid) && Number(pid) > 1;
    if (alive) try { process.kill(Number(pid), 0); }
    catch (error) { if ((error as NodeJS.ErrnoException).code === 'ESRCH') alive = false; }
    if (alive) throw new Error('另一个进程正在安装检查组件，请稍后重试');
    fs.unlinkSync(lock);
  }
  const lockFD = fs.openSync(lock, 'wx', 0o600);
  fs.writeFileSync(lockFD, String(process.pid)); fs.closeSync(lockFD);
  const stage = fs.mkdtempSync(path.join(base, '.install-'));
  try {
    progress('正在安装工具自带的 APK 检查组件，无需配置 Android SDK 或 JDK……');
    await download(RUNTIME_RELEASE.java_url, path.join(stage, 'runtime.tar.gz'), RUNTIME_RELEASE.java_sha256, 200 * 1024 * 1024);
    await download(RUNTIME_RELEASE.apksig_url, path.join(stage, 'apksig.jar'), RUNTIME_RELEASE.apksig_sha256, 2 * 1024 * 1024);
    const archive = path.join(stage, 'runtime.tar.gz');
    const listing = spawnSync('/usr/bin/tar', ['-tzf', archive], { encoding: 'utf8', maxBuffer: 8 * 1024 * 1024, timeout: 30000 });
    if (listing.status !== 0 || listing.stdout.split('\n').filter(Boolean).some(name => path.isAbsolute(name) || name.split('/').includes('..')))
      throw new Error('检查组件压缩包路径异常');
    const extracted = spawnSync('/usr/bin/tar', ['--no-same-owner', '-xzf', archive, '-C', stage], { timeout: 30000 });
    if (extracted.status !== 0) throw new Error('检查组件解压失败');
    fs.unlinkSync(archive);
    const java = path.join(stage, RUNTIME_RELEASE.java);
    const probe = spawnSync(java, ['-version'], { encoding: 'utf8', timeout: 10000 });
    if (probe.status !== 0 || !/21\.0\.13/.test(probe.stderr)) throw new Error('自带运行时启动失败');
    writePrivate(path.join(stage, 'ApkVerifierMain.class'), Buffer.from(VERIFIER_CLASS, 'base64'));
    writePrivate(path.join(stage, 'installation.json'), JSON.stringify({ java_sha256: hashFile(java).sha256 }) + '\n');
    const destination = runtimeDirectory(root);
    if (fs.existsSync(destination)) fs.rmSync(destination, { recursive: true });
    fs.renameSync(stage, destination);
    verifyRuntime(root);
    progress('APK 检查组件已就绪。');
    return new ManagedAndroidTools(root);
  } catch (error) {
    if (error instanceof Error && (error.name === 'TimeoutError' || error.name === 'AbortError' || error.message === 'fetch failed'))
      throw new Error('检查组件下载未完成，请检查网络或代理设置，再运行 android-release doctor --repair');
    throw error;
  } finally {
    fs.rmSync(stage, { recursive: true, force: true });
    fs.unlinkSync(lock);
  }
}

export class ManagedAndroidTools implements ArtifactInspector {
  constructor(readonly root = dataRoot()) {}
  inspect(apk: string): ReturnType<ArtifactInspector['inspect']> {
    let runtime: ReturnType<typeof verifyRuntime>;
    try { runtime = verifyRuntime(this.root); }
    catch { throw new Error('APK 检查组件不可用，请运行 android-release doctor --repair'); }
    const checked = spawnSync(runtime.java, ['-cp', `${path.join(runtime.directory, 'apksig.jar')}${path.delimiter}${runtime.directory}`, 'ApkVerifierMain', apk],
      { encoding: 'utf8', timeout: 120000, maxBuffer: 4096 });
    if (checked.status !== 0 || checked.error) throw new Error('APK 签名验证失败，需提供完整、未篡改且只有一个签名者的发布包');
    const [signer, packageName, versionCode, debug] = checked.stdout.trim().split('\t');
    if (!validHash(signer) || !validPackage(packageName) || !validVersion(versionCode) || !['true', 'false'].includes(debug ?? ''))
      throw new Error('APK 身份信息无效');
    if (debug === 'true') throw new Error('可调试 APK 无法发布');
    return { signer_sha256: signer, package_name: packageName, version_code: versionCode,
      version_name: apkVersionName(apk), abis: apkAbis(apk) };
  }
}
