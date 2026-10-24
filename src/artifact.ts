import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { type Artifact, validHash, validPackage, validVersion } from './model.js';
import { freezeFile, hashFile } from './private-file.js';

function latest(directory: string, file: string): string {
  if (!fs.existsSync(directory)) return '';
  const versions = fs.readdirSync(directory, { withFileTypes: true }).filter(entry => entry.isDirectory())
    .map(entry => entry.name).sort((a, b) => {
      const aa = a.match(/\d+/g)?.map(Number) ?? []; const bb = b.match(/\d+/g)?.map(Number) ?? [];
      for (let i = 0; i < Math.max(aa.length, bb.length); i++) if ((aa[i] ?? 0) !== (bb[i] ?? 0)) return (bb[i] ?? 0) - (aa[i] ?? 0);
      return b.localeCompare(a);
    });
  for (const version of versions) {
    const found = path.join(directory, version, file);
    if (fs.existsSync(found)) return found;
  }
  return '';
}

export class AndroidTools {
  readonly sdkRoot: string;
  readonly javaHome: string;
  readonly apkSigner: string;
  readonly apkAnalyzer: string;
  readonly aapt2: string;
  readonly androidJar: string;

  constructor(sdkRoot = process.env.ANDROID_SDK_ROOT || process.env.ANDROID_HOME || path.join(os.homedir(), 'Library', 'Android', 'sdk'),
    javaHome = process.env.JAVA_HOME || '/Applications/Android Studio.app/Contents/jbr/Contents/Home') {
    this.sdkRoot = sdkRoot;
    this.javaHome = javaHome;
    this.apkSigner = latest(path.join(sdkRoot, 'build-tools'), 'apksigner');
    this.aapt2 = latest(path.join(sdkRoot, 'build-tools'), 'aapt2');
    this.androidJar = latest(path.join(sdkRoot, 'platforms'), 'android.jar');
    this.apkAnalyzer = path.join(sdkRoot, 'cmdline-tools', 'latest', 'bin', 'apkanalyzer');
    if (!fs.existsSync(this.apkAnalyzer)) this.apkAnalyzer = latest(path.join(sdkRoot, 'cmdline-tools'), 'bin/apkanalyzer');
  }

  diagnostic(): Record<string, string | boolean> {
    return { sdk_root: this.sdkRoot, java_home: this.javaHome,
      apksigner: this.apkSigner, apkanalyzer: this.apkAnalyzer, aapt2: this.aapt2,
      android_jar: this.androidJar, ready: Boolean(this.apkSigner && this.apkAnalyzer && this.aapt2 && this.androidJar) };
  }

  command(executable: string, args: string[]): string {
    if (!executable || !fs.existsSync(executable)) throw new Error('Android tool is unavailable; run release doctor');
    const result = spawnSync(executable, args, { encoding: 'utf8', timeout: 30000, maxBuffer: 4 * 1024 * 1024,
      env: { ...process.env, JAVA_HOME: this.javaHome } });
    if (result.status !== 0 || result.error) throw new Error(`${path.basename(executable)} failed; check the SDK, JDK and APK`);
    return result.stdout;
  }

  inspect(apk: string): Omit<Artifact, 'path' | 'filename' | 'sha256' | 'size'> {
    const certs = this.command(this.apkSigner, ['verify', '--verbose', '--print-certs', apk]);
    const counts = [...certs.matchAll(/^Number of signers: ([0-9]+)[ \t\r]*$/gm)];
    if (counts.length !== 1 || counts[0]?.[1] !== '1') throw new Error('APK must have exactly one verified signer');
    const hashes = new Set<string>();
    for (const line of certs.split('\n')) {
      if (!line.includes('certificate SHA-256 digest:') || line.startsWith('Source Stamp ')) continue;
      const match = line.match(/^(?:Signer #1 |V[0-9]+(?:\.[0-9]+)? Signer: )certificate SHA-256 digest: ([a-fA-F0-9]{64})[ \t\r]*$/);
      if (!match) throw new Error('unrecognized APK signer output');
      hashes.add(match[1]!.toLowerCase());
    }
    if (hashes.size !== 1) throw new Error('APK signer changed across signature schemes');
    const query = (verb: string) => this.command(this.apkAnalyzer, ['manifest', verb, apk]).trim();
    const packageName = query('application-id');
    const versionName = query('version-name');
    const versionCode = query('version-code');
    const debuggable = query('debuggable');
    if (!validPackage(packageName) || !validVersion(versionCode) || !versionName || versionName.length > 255 || !['true', 'false'].includes(debuggable)) throw new Error('invalid APK manifest');
    if (debuggable === 'true') throw new Error('debuggable APK cannot be published');
    return { signer_sha256: [...hashes][0]!, package_name: packageName, version_name: versionName,
      version_code: versionCode, abis: apkAbis(apk) };
  }
}

// Read ZIP central-directory names without extracting untrusted APK content.
export function apkAbis(apk: string): string[] {
  const fd = fs.openSync(apk, 'r');
  try {
    const size = fs.fstatSync(fd).size;
    const tailSize = Math.min(size, 65557);
    const tail = Buffer.alloc(tailSize);
    fs.readSync(fd, tail, 0, tailSize, size - tailSize);
    let eocd = -1;
    for (let i = tail.length - 22; i >= 0; i--) if (tail.readUInt32LE(i) === 0x06054b50) { eocd = i; break; }
    if (eocd < 0) throw new Error('APK ZIP directory is missing');
    const count = tail.readUInt16LE(eocd + 10);
    const offset = tail.readUInt32LE(eocd + 16);
    const length = tail.readUInt32LE(eocd + 12);
    if (count === 0xffff || offset === 0xffffffff || length === 0xffffffff || offset + length > size) throw new Error('unsupported ZIP64 APK');
    const names = new Set<string>();
    let pos = offset;
    for (let i = 0; i < count; i++) {
      const head = Buffer.alloc(46);
      if (fs.readSync(fd, head, 0, head.length, pos) !== head.length || head.readUInt32LE(0) !== 0x02014b50) throw new Error('invalid APK ZIP directory');
      const nameSize = head.readUInt16LE(28);
      const extraSize = head.readUInt16LE(30);
      const commentSize = head.readUInt16LE(32);
      if (nameSize > 4096 || pos + 46 + nameSize + extraSize + commentSize > size) throw new Error('invalid APK ZIP entry');
      const name = Buffer.alloc(nameSize);
      fs.readSync(fd, name, 0, nameSize, pos + 46);
      const found = name.toString('utf8').match(/^lib\/([^/]+)\/[^/]+\.so$/);
      if (found) names.add(found[1]!);
      pos += 46 + nameSize + extraSize + commentSize;
    }
    return [...names].sort();
  } finally { fs.closeSync(fd); }
}

export function inspectAndFreeze(tools: AndroidTools, source: string, root: string): Artifact {
  const hashed = hashFile(source);
  const inspected = tools.inspect(source);
  const after = hashFile(source);
  if (hashed.sha256 !== after.sha256 || hashed.size !== after.size) throw new Error('APK changed while being inspected');
  const frozen = path.join(root, 'artifacts', hashed.sha256, 'blob.apk');
  freezeFile(source, frozen, hashed.sha256);
  if (!validHash(inspected.signer_sha256)) throw new Error('invalid APK signer hash');
  return { path: frozen, filename: path.basename(source), sha256: hashed.sha256, size: hashed.size, ...inspected };
}

export function verifyFrozen(tools: AndroidTools, artifact: Artifact): void {
  const hashed = hashFile(artifact.path);
  if (hashed.sha256 !== artifact.sha256 || hashed.size !== artifact.size) throw new Error('frozen APK differs from the reviewed artifact');
  const current = tools.inspect(artifact.path);
  for (const key of ['signer_sha256', 'package_name', 'version_name', 'version_code'] as const)
    if (current[key] !== artifact[key]) throw new Error('frozen APK identity changed');
}
