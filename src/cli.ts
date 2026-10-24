#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import readline from 'node:readline/promises';
import { AndroidTools } from './artifact.js';
import { FileCredentials, PromptCredentials, type CredentialSource } from './credentials.js';
import { loadProject, prepare, preflight, publicError, reconcile, report, review, saveProject, submit, refresh } from './engine.js';
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
function credentialSource(f: Flags): CredentialSource {
  const file = f['credentials-file'];
  if (!file) return new PromptCredentials();
  if (typeof file !== 'string' || !validAbsolute(file)) throw new Error('credentials file must be an absolute path');
  return new FileCredentials(file);
}
function output(value: unknown): void { process.stdout.write(`${JSON.stringify(value)}\n`); }
function toolFlags(f: Flags): AndroidTools {
  const sdk = typeof f['sdk-root'] === 'string' ? f['sdk-root'] : undefined;
  const java = typeof f['java-home'] === 'string' ? f['java-home'] : undefined;
  return new AndroidTools(sdk, java);
}

function help(): void {
  process.stdout.write(`Android Publisher CLI (TypeScript + npm)\n\n` +
    `release doctor [--sdk-root DIR] [--java-home DIR]\n` +
    `release inspect --apk /absolute/file.apk\n` +
    `release setup --project ID --apk /absolute/trusted.apk\n` +
    `release setup --file /absolute/project.json --apk /absolute/trusted.apk [--replace]\n` +
    `release prepare --file /absolute/batch.json\n` +
    `release batch preflight --batch ID [--channels LIST] [--credentials-file FILE]\n` +
    `release batch review --batch ID --file /absolute/approval.json [--channels LIST]\n` +
    `release batch submit|resume --batch ID --approval FILE --approve-plan SHA256 [--credentials-file FILE]\n` +
    `release batch status --batch ID [--refresh] [--credentials-file FILE]\n` +
    `release batch reconcile --batch ID [--credentials-file FILE]\n` +
    `release batch report --batch ID --file /absolute/report.md\n` +
    `release history | protocols | version [--json]\n\n` +
    `Credentials are requested in the terminal by default. A 0600 JSON file is supported for automation.\n`);
}

async function setupWizard(projectId: string, apk: string, tools: AndroidTools): Promise<Project> {
  if (!validId(projectId) || !validAbsolute(apk)) throw new Error('project ID or trusted APK path is invalid');
  const inspected = tools.inspect(apk);
  const io = readline.createInterface({ input: process.stdin, output: process.stderr });
  const ask = async (label: string, fallback = ''): Promise<string> => {
    const typed = (await io.question(`${label}${fallback ? ` [${fallback}]` : ''}：`)).trim();
    if (typed.toLowerCase() === 'cancel') throw new Error('setup canceled');
    return typed || fallback;
  };
  try {
    process.stderr.write(`包名：${inspected.package_name}\n签名 SHA-256：${inspected.signer_sha256}\n`);
    if ((await ask('确认信任这个包名和签名，输入 yes')) !== 'yes') throw new Error('setup canceled');
    const selection = channelsFlag(await ask('选择渠道，逗号分隔', CHANNELS.join(',')))!;
    const rows: ProjectChannel[] = [];
    for (const channel of selection) {
      process.stderr.write(`${channel}：只输入公开账号和应用标识。凭据在执行命令时另行输入。\n`);
      const account = await ask(channel === 'oppo' ? 'OPPO client_id（也作为本机账号名）' : '本机账号名', channel === 'oppo' ? '' : `${channel}-main`);
      const appId = await ask('渠道应用 ID；小米、OPPO、vivo 使用包名',
        ['xiaomi', 'oppo', 'vivo'].includes(channel) ? inspected.package_name : '');
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
    const project: Project = { schema_version: 2, project_id: projectId, package_name: inspected.package_name,
      signer_sha256: inspected.signer_sha256, channels: rows };
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
    const f = flags(args.slice(1), ['file', 'project', 'apk', 'sdk-root', 'java-home'], ['replace']);
    const apk = requiredFlag(f, 'apk');
    if (Boolean(f.file) === Boolean(f.project)) throw new Error('choose --file or --project');
    const tools = toolFlags(f);
    let project: Project;
    if (f.file) {
      project = validateProject(readJSON(requiredFlag(f, 'file'), 64 * 1024));
      const trusted = tools.inspect(apk);
      if (project.package_name !== trusted.package_name || project.signer_sha256 !== trusted.signer_sha256) throw new Error('project file differs from trusted APK');
    } else project = await setupWizard(requiredFlag(f, 'project'), apk, tools);
    saveProject(project, undefined, Boolean(f.replace));
    output({ schema_version: 2, project_id: project.project_id, package_name: project.package_name,
      signer_sha256: project.signer_sha256, channels: project.channels.map(row => row.channel), saved: true });
    return 0;
  }
  if (command === 'prepare') {
    const f = flags(args.slice(1), ['file', 'sdk-root', 'java-home']);
    const input = validateBatchInput(readJSON(requiredFlag(f, 'file'), 64 * 1024));
    const store = new Store();
    try { const batch = prepare(input, toolFlags(f), store); output({ ...store.summary(batch.batch_id), effect: 'offline' }); return 0; }
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
      preflight: ['batch', 'channels', 'credentials-file'],
      review: ['batch', 'channels', 'file'],
      submit: ['batch', 'approval', 'approve-plan', 'credentials-file', 'sdk-root', 'java-home'],
      resume: ['batch', 'approval', 'approve-plan', 'credentials-file', 'sdk-root', 'java-home'],
      status: ['batch', 'channels', 'credentials-file'],
      reconcile: ['batch', 'channels', 'credentials-file'],
      report: ['batch', 'file'],
    };
    if (!values[action]) throw new Error('unsupported batch action');
    const f = flags(args.slice(2), values[action], action === 'status' ? ['refresh'] : []);
    const batchId = requiredFlag(f, 'batch');
    const selection = channelsFlag(f.channels);
    if (action === 'status' && !f.refresh && (selection?.length || f['credentials-file'])) throw new Error('cached status does not use channels or credentials');
    const store = new Store();
    try {
      if (action === 'preflight') {
        const result = await preflight(store, batchId, credentialSource(f), selection);
        output(result); return Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'review') {
        const approval = review(store, batchId, requiredFlag(f, 'file'), selection);
        output({ schema_version: 2, effect: 'offline', approval }); return 0;
      }
      if (action === 'submit' || action === 'resume') {
        if (selection?.length) throw new Error('channel selection is bound by the approval file');
        const result = await submit(store, batchId, requiredFlag(f, 'approval'), requiredFlag(f, 'approve-plan'),
          credentialSource(f), toolFlags(f), action === 'resume');
        output(result); return Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'status') {
        const result = f.refresh ? await refresh(store, batchId, credentialSource(f), selection) : { ...store.summary(batchId), effect: 'cached' };
        output(result); return 'errors' in result && Object.keys(result.errors as object).length ? 3 : 0;
      }
      if (action === 'reconcile') {
        const result = await reconcile(store, batchId, credentialSource(f), selection);
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
  throw new Error('unsupported command; run release --help');
}

run(process.argv.slice(2)).then(code => { process.exitCode = code; }).catch(error => {
  output({ schema_version: 2, error: publicError(error), effect: 'not_sent', next_action: 'check_input_or_reconcile' });
  process.exitCode = 2;
});
