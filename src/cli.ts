#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import readline from 'node:readline/promises';
import { AndroidTools } from './artifact.js';
import { diagnose, doctorText } from './doctor.js';
import { configureProject } from './setup.js';
import { PromptCredentials } from './credentials.js';
import { protocols, protocolsText } from './channels/capabilities.js';
import { loadProject, projectPath, prepare, previewBatch, preflight, publicError, reconcile, report, review, saveProject, submit, refresh } from './engine.js';
import { type Channel, type Project, isChannel, validateBatchInput, validAbsolute } from './model.js';
import { dataRoot, readJSON, writePrivate } from './private-file.js';
import { Store } from './state.js';

type Flags = Record<string, string | boolean>;
function flags(args: string[], values: string[], booleans: string[] = []): Flags {
  const result: Flags = {};
  for (let i = 0; i < args.length; i++) {
    const name = args[i];
    if (!name?.startsWith('--') || name === '--') throw new Error(`unexpected argument: ${name ?? ''}`);
    const key = name.slice(2);
    if (key in result || !values.includes(key) && !booleans.includes(key)) throw new Error(`unsupported or duplicate flag: ${name}`);
    if (booleans.includes(key)) result[key] = true;
    else {
      const value = args[++i];
      if (!value || value.startsWith('--')) throw new Error(`${name} needs a value`);
      result[key] = value;
    }
  }
  return result;
}
function requiredFlag(f: Flags, key: string): string {
  const value = f[key];
  if (typeof value !== 'string' || !value) throw new Error(`--${key} is required`);
  return value;
}
function channelsFlag(value: string | boolean | undefined): Channel[] | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== 'string') throw new Error('invalid channel selection');
  const channels = value.split(',').map(item => item.trim());
  if (!channels.length || new Set(channels).size !== channels.length || channels.some(ch => !isChannel(ch))) throw new Error('unknown or duplicate channel');
  return channels as Channel[];
}
function output(value: unknown): void { process.stdout.write(`${JSON.stringify(value)}\n`); }
function toolFlags(f: Flags): AndroidTools {
  const sdk = typeof f['sdk-root'] === 'string' ? f['sdk-root'] : undefined;
  const java = typeof f['java-home'] === 'string' ? f['java-home'] : undefined;
  return new AndroidTools(sdk, java);
}

function help(): void {
  process.stdout.write(`Android 发布工具：配置应用 → 准备发布 → 检查渠道 → 核对计划 → 提交审核 → 查询结果\n\n` +
    `android-release doctor [--sdk-root DIR] [--java-home DIR] [--json]  检查本机工具并给出修复方法\n` +
    `android-release inspect --apk /absolute/file.apk\n` +
    `android-release setup --project ID [--replace]\n` +
    `android-release prepare --file /absolute/batch.json\n` +
    `android-release batch preflight --batch ID [--channels LIST]\n` +
    `android-release batch review --batch ID --file /absolute/approval.json [--channels LIST]\n` +
    `android-release batch submit|resume --batch ID --approval FILE --approve-plan SHA256\n` +
    `android-release batch status --batch ID [--refresh]\n` +
    `android-release batch reconcile --batch ID\n` +
    `android-release batch report --batch ID --file /absolute/report.md\n` +
    `android-release history\n` +
    `android-release protocols [--json]  查看渠道能力、限制和验证范围\n` +
    `android-release version [--json]\n\n` +
    `setup 保存应用配置；prepare 保存本次 APK 和更新说明，生成发布批次。\n` +
    `batch review 生成供你核对的提交计划；submit 会上传 APK 并提审。\n` +
    `密钥通过终端输入。JSON 和 PEM 材料按提示输入私有文件的绝对路径。\n`);
}

async function setupWizard(projectId: string, previous?: Project): Promise<Project> {
  const io = readline.createInterface({ input: process.stdin, output: process.stderr });
  try {
    return await configureProject(projectId, {
      ask: (label, fallback) => io.question(`${label}${fallback ? ` [${fallback}]` : ''}：`),
      write: message => { process.stderr.write(message); },
    }, previous);
  } finally { io.close(); }
}

async function run(args: string[]): Promise<number> {
  if (!args.length || args[0] === 'help' || args[0] === '--help' || args[0] === '-h') { help(); return 0; }
  const command = args[0];
  if (command === 'version' || command === '--version') {
    const f = flags(args.slice(1), [], ['json']);
    const packagePath = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'package.json');
    const version = JSON.parse(fs.readFileSync(packagePath, 'utf8')).version as string;
    if (f.json) output({ schema_version: 2, version, runtime: process.version, arch: process.arch, platform: process.platform });
    else process.stdout.write(`${version}\n`);
    return 0;
  }
  if (command === 'doctor') {
    const f = flags(args.slice(1), ['sdk-root', 'java-home'], ['json']);
    const diagnostic = diagnose(toolFlags(f));
    if (f.json) output(diagnostic); else process.stdout.write(doctorText(diagnostic));
    return diagnostic.ready ? 0 : 2;
  }
  if (command === 'inspect') {
    const f = flags(args.slice(1), ['apk', 'sdk-root', 'java-home']);
    output({ schema_version: 2, ...toolFlags(f).inspect(requiredFlag(f, 'apk')) }); return 0;
  }
  if (command === 'protocols') {
    const f = flags(args.slice(1), [], ['json']);
    if (f.json) output({ schema_version: 2, channels: protocols() });
    else process.stdout.write(protocolsText());
    return 0;
  }
  if (command === 'setup') {
    const f = flags(args.slice(1), ['project'], ['replace']);
    const id = requiredFlag(f, 'project');
    const exists = fs.existsSync(projectPath(dataRoot(), id));
    if (exists && !f.replace) throw new Error('应用配置已存在。如需修改，请用 android-release setup --project ' + id + ' --replace');
    const project = await setupWizard(id, exists ? loadProject(id) : undefined);
    saveProject(project, undefined, Boolean(f.replace));
    output({ schema_version: 2, project_id: project.project_id,
      channels: project.channels.map(row => row.channel), saved: true });
    return 0;
  }
  if (command === 'prepare') {
    const f = flags(args.slice(1), ['file', 'sdk-root', 'java-home']);
    const input = validateBatchInput(readJSON(requiredFlag(f, 'file'), 64 * 1024));
    const store = new Store();
    try {
      const tools = toolFlags(f);
      const preview = previewBatch(input, tools, store);
      process.stderr.write(`包名：${preview.package_name}\n版本名（versionName）：${preview.version_name}\n版本代码（versionCode）：${preview.version_code}\n`);
      if (!process.stdin.isTTY) throw new Error('prepare confirmation requires a terminal');
      const io = readline.createInterface({ input: process.stdin, output: process.stderr });
      try {
        if ((await io.question('确认包名、版本名和版本代码，输入 yes 保存批次：')).trim() !== 'yes') throw new Error('prepare canceled');
      } finally { io.close(); }
      const batch = prepare(input, tools, store, preview);
      output({ ...store.summary(batch.batch_id), effect: 'offline' }); return 0;
    }
    finally { store.close(); }
  }
  if (command === 'history') {
    if (args.length !== 1) throw new Error('history takes no flags');
    const store = new Store();
    try { output({ schema_version: 2, batches: store.batchIds().map(id => store.summary(id)) }); return 0; }
    finally { store.close(); }
  }
  if (command === 'batch') {
    const action = args[1];
    if (!action) throw new Error('batch action is required');
    const values: Record<string, string[]> = {
      preflight: ['batch', 'channels'],
      review: ['batch', 'channels', 'file'],
      submit: ['batch', 'approval', 'approve-plan', 'sdk-root', 'java-home'],
      resume: ['batch', 'approval', 'approve-plan', 'sdk-root', 'java-home'],
      status: ['batch', 'channels'],
      reconcile: ['batch', 'channels'],
      report: ['batch', 'file'],
    };
    if (!values[action]) throw new Error('unsupported batch action');
    const f = flags(args.slice(2), values[action], action === 'status' ? ['refresh'] : []);
    const batchId = requiredFlag(f, 'batch');
    const selection = channelsFlag(f.channels);
    if (action === 'status' && !f.refresh && selection?.length) throw new Error('cached status does not use channels');
    const store = new Store();
    try {
      if (action === 'preflight') {
        const result = await preflight(store, batchId, new PromptCredentials(), selection);
        output(result); return Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'review') {
        const approval = review(store, batchId, requiredFlag(f, 'file'), selection);
        output({ schema_version: 2, effect: 'offline', approval }); return 0;
      }
      if (action === 'submit' || action === 'resume') {
        if (selection?.length) throw new Error('channel selection is bound by the approval file');
        const result = await submit(store, batchId, requiredFlag(f, 'approval'), requiredFlag(f, 'approve-plan'),
          new PromptCredentials(), toolFlags(f), action === 'resume');
        output(result); return Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'status') {
        const result = f.refresh ? await refresh(store, batchId, new PromptCredentials(), selection) : { ...store.summary(batchId), effect: 'cached' };
        output(result); return 'errors' in result && Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'reconcile') {
        const result = await reconcile(store, batchId, new PromptCredentials(), selection);
        output(result); return Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'report') {
        const file = requiredFlag(f, 'file');
        if (!validAbsolute(file)) throw new Error('report file path must be absolute');
        writePrivate(file, report(store, batchId));
        output({ schema_version: 2, effect: 'read_only', report: file }); return 0;
      }
      throw new Error('unsupported batch action');
    } finally { store.close(); }
  }
  throw new Error('unsupported command; run android-release --help');
}

run(process.argv.slice(2)).then(code => { process.exitCode = code; }).catch(error => {
  output({ schema_version: 2, error: publicError(error), effect: 'not_sent', next_action: 'check_input_or_reconcile' });
  process.exitCode = 2;
});
