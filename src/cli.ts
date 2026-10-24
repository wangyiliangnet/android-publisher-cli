#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import readline from 'node:readline/promises';
import { AndroidTools } from './artifact.js';
import { PromptCredentials } from './credentials.js';
import { prepare, previewBatch, preflight, publicError, reconcile, report, review, saveProject, submit, refresh } from './engine.js';
import { CHANNELS, type Channel, type Project, type ProjectChannel, isChannel, validateBatchInput, validateProject, validAbsolute, validId } from './model.js';
import { readJSON, writePrivate } from './private-file.js';
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
  process.stdout.write(`Android Publisher CLI (TypeScript + npm)\n\n` +
    `android-release doctor [--sdk-root DIR] [--java-home DIR]\n` +
    `android-release inspect --apk /absolute/file.apk\n` +
    `android-release setup --project ID [--replace]\n` +
    `android-release prepare --file /absolute/batch.json\n` +
    `android-release batch preflight --batch ID [--channels LIST]\n` +
    `android-release batch review --batch ID --file /absolute/approval.json [--channels LIST]\n` +
    `android-release batch submit|resume --batch ID --approval FILE --approve-plan SHA256\n` +
    `android-release batch status --batch ID [--refresh]\n` +
    `android-release batch reconcile --batch ID\n` +
    `android-release batch report --batch ID --file /absolute/report.md\n` +
    `android-release history | protocols | version [--json]\n\n` +
    `Credentials are requested in the terminal. Multiline JSON and PEM inputs use private files.\n`);
}

async function setupWizard(projectId: string): Promise<Project> {
  if (!validId(projectId)) throw new Error('project ID is invalid');
  const io = readline.createInterface({ input: process.stdin, output: process.stderr });
  const ask = async (label: string, fallback = ''): Promise<string> => {
    const typed = (await io.question(`${label}${fallback ? ` [${fallback}]` : ''}：`)).trim();
    if (typed.toLowerCase() === 'cancel') throw new Error('setup canceled');
    return typed || fallback;
  };
  try {
    const selection = channelsFlag(await ask('选择渠道，逗号分隔', CHANNELS.join(',')))!;
    const rows: ProjectChannel[] = [];
    for (const channel of selection) {
      process.stderr.write(`${channel}：只输入公开账号和应用标识。凭据在执行命令时另行输入。\n`);
      const account = await ask(channel === 'oppo' ? 'OPPO client_id（也作为本机账号名）' : '本机账号名', channel === 'oppo' ? '' : `${channel}-main`);
      const appId = await ask('渠道应用 ID；小米、OPPO、vivo 填包名');
      const row: ProjectChannel = { channel, account, app_id: appId };
      const clientLabels: Partial<Record<Channel, string>> = { honor: 'Honor client_id', xiaomi: '小米 userName',
        vivo: 'vivo access_key', tencent: '应用宝用户 uid' };
      if (clientLabels[channel]) row.client_id = await ask(clientLabels[channel]!);
      if (channel === 'huawei' || channel === 'honor') {
        const language = await ask('中文语言 ID，可留空自动匹配');
        if (language) row.language_id = language;
      }
      if (channel === 'xiaomi') {
        row.icon_path = await ask('小米图标绝对路径');
        row.privacy_url = await ask('小米公开 HTTPS 隐私政策 URL');
      }
      rows.push(row);
    }
    const project: Project = { schema_version: 2, project_id: projectId, channels: rows };
    validateProject(project);
    process.stderr.write(`项目摘要：${JSON.stringify(project)}\n`);
    if ((await ask('确认保存，输入 yes')) !== 'yes') throw new Error('setup canceled');
    return project;
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
    const f = flags(args.slice(1), ['sdk-root', 'java-home']);
    const diagnostic = toolFlags(f).diagnostic();
    output({ schema_version: 2, ...diagnostic }); return diagnostic.ready ? 0 : 2;
  }
  if (command === 'inspect') {
    const f = flags(args.slice(1), ['apk', 'sdk-root', 'java-home']);
    output({ schema_version: 2, ...toolFlags(f).inspect(requiredFlag(f, 'apk')) }); return 0;
  }
  if (command === 'protocols') {
    if (args.length !== 1) throw new Error('protocols takes no flags');
    output({ schema_version: 2, channels: CHANNELS.map(channel => ({ channel,
      protocol_mock_verified: true, account_verified: false, live_verified: false,
      note: 'Real account access and lost-response reconciliation require separate evidence.' })) }); return 0;
  }
  if (command === 'setup') {
    const f = flags(args.slice(1), ['project'], ['replace']);
    const project = await setupWizard(requiredFlag(f, 'project'));
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
