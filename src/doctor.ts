import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { AndroidTools } from './artifact.js';

interface Check {
  id: string;
  name: string;
  status: 'ok' | 'missing' | 'failed' | 'blocked';
  path: string;
  message: string;
  fixes: string[];
}

export interface DoctorResult {
  schema_version: 2;
  sdk_root: string;
  java_home: string;
  apksigner: string;
  apkanalyzer: string;
  aapt2: string;
  android_jar: string;
  ready: boolean;
  checks: Check[];
}

const sdkManager = 'Android Studio → Tools → SDK Manager';
const sdkFix = '核对 SDK Manager 中的 Android SDK Location。若 SDK 装在其他目录，用 --sdk-root 指定该目录；后续命令可通过 ANDROID_SDK_ROOT 使用同一目录。';
const javaFix = '使用与 Android 工具兼容的 JDK（本项目使用 JDK 21 验证）。用 --java-home 指定 JDK 主目录，或设置 JAVA_HOME；目录下应有 bin/java。Android Studio 自带 JDK 通常位于 /Applications/Android Studio.app/Contents/jbr/Contents/Home。';

function accessible(file: string, directory = false): boolean {
  try {
    const stat = fs.statSync(file);
    fs.accessSync(file, fs.constants.R_OK | (directory ? fs.constants.X_OK : 0));
    return directory ? stat.isDirectory() : stat.isFile();
  } catch { return false; }
}

export function diagnose(tools: AndroidTools): DoctorResult {
  const checks: Check[] = [];
  const sdkOK = accessible(tools.sdkRoot, true);
  checks.push({ id: 'sdk_root', name: 'Android SDK 目录', status: sdkOK ? 'ok' : 'missing', path: tools.sdkRoot,
    message: sdkOK ? '目录可读取' : '目录不存在或无法读取', fixes: sdkOK ? [] : [sdkFix,
      `尚未安装 SDK 时，在 ${sdkManager} 安装 SDK。官方步骤：https://developer.android.com/studio/intro/update#sdk-manager`] });

  const probe = (id: string, name: string, file: string, args: string[], fixes: string[], needsJava = false): Check => {
    const base = { id, name, path: file };
    if (id !== 'java' && !sdkOK) return { ...base, status: 'blocked', message: '请先修复 SDK 目录，再检查此工具', fixes: [] };
    if (!file || !accessible(file)) return { ...base, status: 'missing', message: '未找到可读取的工具文件',
      fixes: id === 'java' ? fixes : fixes.filter(fix => fix !== javaFix) };
    try { fs.accessSync(file, fs.constants.X_OK); }
    catch { return { ...base, status: 'failed', message: '工具没有执行权限',
      fixes: [`运行 chmod u+x '${file.replaceAll("'", "'\\''")}' 后重新检查。`] }; }
    if (needsJava && checks.find(check => check.id === 'java')?.status !== 'ok')
      return { ...base, status: 'blocked', message: '文件已找到；请先修复 Java，再检查能否启动', fixes: [] };
    const result = spawnSync(file, args, { encoding: 'utf8', timeout: 5000, maxBuffer: 256 * 1024,
      env: { ...process.env, JAVA_HOME: tools.javaHome } });
    if (result.error || result.status !== 0) {
      const reason = result.error && 'code' in result.error && result.error.code === 'ETIMEDOUT'
        ? '启动检查超过 5 秒' : needsJava ? '启动失败，请检查 JDK 兼容性和工具安装完整性'
          : id === 'java' ? 'Java 启动失败，请检查 JDK 安装和 CPU 架构' : '启动失败，请检查 SDK 组件安装和 CPU 架构';
      return { ...base, status: 'failed', message: reason, fixes };
    }
    return { ...base, status: 'ok', message: '启动检查通过', fixes: [] };
  };

  checks.push(probe('java', 'Java', path.join(tools.javaHome, 'bin', 'java'), ['-version'], [javaFix]));
  const buildFix = `在 ${sdkManager} → SDK Tools 勾选 Android SDK Build-Tools 并点击 Apply。安装到上方 SDK 目录；已安装时重新安装对应版本。`;
  checks.push(probe('apksigner', 'APK 签名检查工具（apksigner）', tools.apkSigner, ['version'], [buildFix, sdkFix, javaFix], true));
  checks.push(probe('apkanalyzer', 'APK 信息读取工具（apkanalyzer）', tools.apkAnalyzer, ['--help'], [
    `在 ${sdkManager} → SDK Tools 勾选 Android SDK Command-line Tools (latest)，点击 Apply。这个工具包包含 apkanalyzer。`,
    '安装后应有 cmdline-tools/latest/bin/apkanalyzer 或 cmdline-tools/<版本>/bin/apkanalyzer。手动解压时，bin 和 lib 应位于同一个版本目录下。',
    sdkFix, javaFix, '官方说明：https://developer.android.com/tools/apkanalyzer'], true));
  checks.push(probe('aapt2', 'Android 资源工具（aapt2）', tools.aapt2, ['version'], [buildFix, sdkFix]));
  const jarOK = Boolean(tools.androidJar && accessible(tools.androidJar));
  checks.push({ id: 'android_jar', name: 'Android 平台文件（android.jar）', status: !sdkOK ? 'blocked' : jarOK ? 'ok' : 'missing', path: tools.androidJar,
    message: !sdkOK ? '请先修复 SDK 目录，再检查此文件' : jarOK ? '文件可读取' : '未找到可读取的平台文件', fixes: jarOK || !sdkOK ? [] : [
      `在 ${sdkManager} → SDK Platforms 安装一个 Android SDK Platform（例如 Android 16 / API 36）。`, sdkFix] });
  return { schema_version: 2, sdk_root: tools.sdkRoot, java_home: tools.javaHome,
    apksigner: tools.apkSigner, apkanalyzer: tools.apkAnalyzer, aapt2: tools.aapt2, android_jar: tools.androidJar,
    ready: checks.every(check => check.status === 'ok'), checks };
}

export function doctorText(result: DoctorResult): string {
  const labels = { ok: '通过', missing: '缺失', failed: '失败', blocked: '待检查' };
  const lines = ['Android 发布环境检查', `SDK 目录：${result.sdk_root}`, `JDK 目录：${result.java_home}`, ''];
  for (const check of result.checks) {
    lines.push(`[${labels[check.status]}] ${check.name}：${check.message}`);
    if (check.path) lines.push(`  路径：${check.path}`);
    for (const fix of check.fixes) lines.push(`  修复：${fix}`);
  }
  lines.push('', result.ready ? '环境检查通过。可以检查 APK 或准备发布。'
    : '环境检查未通过。修复后重新运行 android-release doctor。');
  return `${lines.join('\n')}\n`;
}
