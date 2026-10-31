#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import readline from 'node:readline/promises';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { setGlobalProxyFromEnv } from 'node:http';
import { PublisherApplication, type ReleaseView } from './application.js';
import { PromptCredentials } from './credentials.js';
import { publicError } from './engine.js';
import { type Channel, CHANNELS, isChannel, validId } from './model.js';
import { dataRoot, regularFile } from './private-file.js';
import { protocols, protocolsText } from './channels/capabilities.js';
import { configureProject, channelNames } from './setup.js';
import { ensureRuntime, runtimeDiagnostic } from './runtime.js';
import { startWebServer } from './web.js';

type Flags = { [key: string]: string | boolean | string[] };
const valueFlags = ['apk', 'channels', 'notes-file', 'release', 'port'];
const boolFlags = ['json', 'cached', 'record-manual', 'repair', 'no-open', 'reconcile'];
function parse(args: string[]): { positional: string[]; flags: Flags } {
  const flags: Flags = {}, positional: string[] = [];
  for (let n = 0; n < args.length; n++) {
    const item = args[n]!;
    if (!item.startsWith('--')) { positional.push(item); continue; }
    const key = item.slice(2);
    if ((!valueFlags.includes(key) && !boolFlags.includes(key)) || key in flags && key !== 'apk') throw new Error(`未知或重复参数 ${item}；用 --help 查看用法`);
    if (boolFlags.includes(key)) { flags[key] = true; continue; }
    const value = args[++n];
    if (!value || value.startsWith('--')) throw new Error(`${item} 需要一个值`);
    if (key === 'apk') flags.apk = [...(flags.apk as string[] ?? []), value]; else flags[key] = value;
  }
  return { positional, flags };
}
function allow(flags: Flags, allowed: string[]): void {
  for (const key of Object.keys(flags)) if (!allowed.includes(key)) throw new Error(`此命令不支持 --${key}`);
}
function selection(value: unknown): Channel[] | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== 'string') throw new Error('渠道列表无效');
  const channels = value.split(',').map(row => row.trim());
  if (!channels.length || channels.some(row => !isChannel(row)) || new Set(channels).size !== channels.length) throw new Error('渠道名称无效或重复');
  return channels as Channel[];
}
export class Canceled extends Error { constructor() { super('已取消，已有发布记录保留。'); } }
export interface TerminalIO {
  ask(label: string, fallback?: string): Promise<string>;
  write(message: string): void;
  multiline(): Promise<string>;
  suspend(): void;
  close(): void;
}
class Terminal implements TerminalIO {
  private io: readline.Interface | undefined;
  private input(): readline.Interface {
    if (!process.stdin.isTTY) throw new Error('此操作需要终端交互。查询可使用 --json。');
    if (!this.io) {
      this.io = readline.createInterface({ input: process.stdin, output: process.stderr });
      this.io.on('SIGINT', () => { this.io?.close(); process.kill(process.pid, 'SIGINT'); });
    }
    return this.io;
  }
  async ask(label: string, fallback = ''): Promise<string> {
    const input = this.input();
    let onClose: (() => void) | undefined;
    const closed = new Promise<string>((_resolve, reject) => { onClose = () => reject(new Canceled()); input.once('close', onClose); });
    let answer: string;
    try { answer = (await Promise.race([input.question(`${label}${fallback ? ` [${fallback}]` : ''}：`), closed])).trim(); }
    finally { if (onClose) input.removeListener('close', onClose); }
    if (answer.toLowerCase() === 'cancel') throw new Canceled();
    return answer || fallback;
  }
  write(message: string): void { process.stderr.write(message); }
  async multiline(): Promise<string> {
    const io = this.input();
    this.write('输入或粘贴更新说明，单独一行输入 :end 完成：\n');
    return new Promise((resolve, reject) => {
      const lines: string[] = [];
      const finish = () => { io.removeListener('line', onLine); io.removeListener('close', onClose); };
      const onClose = () => { finish(); reject(new Canceled()); };
      const onLine = (line: string) => {
        if (line === ':end') { finish(); resolve(lines.join('\n')); }
        else if (Buffer.byteLength(lines.join('\n')) + Buffer.byteLength(line) > 65536) { finish(); reject(new Error('更新说明过长')); }
        else lines.push(line);
      };
      io.on('line', onLine); io.once('close', onClose);
    });
  }
  suspend(): void { this.io?.close(); this.io = undefined; }
  close(): void { this.suspend(); }
}
const labels: Record<string, string> = { prepared: '待检查', preflighted: '待提交', submitted: '已受理', unknown: '未确认',
  in_review: '审核中', approved: '审核通过', rejected: '审核拒绝', live: '已上架', not_live: '未上架',
  never: '未查询', ok: '成功', failed: '失败' };
function width(text: string): number { return [...text].reduce((total, ch) => total + (ch.codePointAt(0)! > 255 ? 2 : 1), 0); }
function table(rows: string[][]): string {
  const widths = rows[0]!.map((_, i) => Math.max(...rows.map(row => width(row[i] ?? ''))));
  return rows.map(row => row.map((cell, i) => cell + ' '.repeat(widths[i]! - width(cell))).join('  ').trimEnd()).join('\n') + '\n';
}
export function releaseText(release: ReleaseView): string {
  const rows = [['渠道', '提交', '审核', '上架', '查询']];
  for (const target of release.targets) {
    const ch = target.channel as Channel;
    const support = target.query_support as { review: boolean; availability: boolean };
    const state = (key: string) => labels[String(target[key])] ?? String(target[key]);
    const submitted = target.execution_phase === 'submitted';
    rows.push([channelNames[ch], submitted ? '商店已接收' : target.recovery_required ? '提交结果待确认' : state('execution_phase'),
      !submitted ? '尚未确认提交' : target.review_state === 'unknown' && !support.review ? '需到商店查看' : state('review_state'),
      !submitted ? '尚未确认提交' : target.availability_state === 'unknown' && !support.availability ? '需到商店查看' : state('availability_state'), state('query_status')]);
  }
  const manual = release.manual_observations as unknown[] ?? [];
  const lines = [`应用：${release.display_name ?? release.project_id}  包名：${release.package_name}`, `版本：${release.version_name}（${release.version_code}）`,
    `发布记录：${release.batch_id}`, '', table(rows),
    `受理 ${release.accepted_targets}/${release.total_targets}；自动确认审核通过 ${release.approved_targets}；自动确认上架 ${release.live_targets}；人工记录 ${manual.length} 条。`];
  for (const target of release.targets) {
    if (target.query_status === 'failed') lines.push(`${channelNames[target.channel as Channel]}：本次查询失败；上次成功时间 ${target.last_success_at || '尚无'}。`);
    for (const issue of target.recovery as Array<Record<string, unknown>>) lines.push(`${channelNames[target.channel as Channel]}：${issue.message}\n下一步：${issue.next_action}`);
    for (const observation of target.manual_observations as Array<Record<string, unknown>> ?? [])
      lines.push(`${channelNames[target.channel as Channel]}（人工核对）：审核 ${labels[String(observation.review_state)]}；上架 ${labels[String(observation.availability_state)]}；${observation.observed_at}；来源：${observation.source}`);
  }
  if (release.all_accepted) lines.push('提交已完成。用 android-release status ' + release.project_id + ' 跟踪审核和上架。');
  else if (release.recovery_required) lines.push('部分提交结果待确认，请先检查：android-release status ' + release.project_id + ' --reconcile\n其他可安全处理的渠道可通过 android-release publish ' + release.project_id + ' 继续，原发布范围保留。');
  else lines.push('继续未完成的提交：android-release publish ' + release.project_id);
  return lines.join('\n') + '\n';
}
function help(context = ''): string {
  const intro = 'Android 发布工具：配置应用 → 指定渠道包 → 确认发布 → 查询结果\n\n';
  const descriptions: Record<string, string> = {
    app: 'android-release app add [应用]\nandroid-release app edit [应用]\nandroid-release app list [--json]\n\n配置长期应用资料和渠道账号，首次配置无需 APK。\n',
    publish: 'android-release publish [应用] [--channels huawei,honor] [--apk huawei=/绝对路径/one.apk] [--notes-file /绝对路径/notes.txt]\n\n默认全部已配置渠道，逐渠道明确指定 APK。工具检查材料与渠道，展示摘要，确认一次后提交。再次进入可继续已有发布。\n',
    status: 'android-release status [应用] [--release 记录] [--cached] [--json] [--record-manual] [--reconcile]\n\n默认查询最近一次发布。--cached 只读本机记录；--record-manual 记录人工核对；--reconcile 只读取能够核对未决请求的远端证据。\n',
    history: 'android-release history [应用] [--json]\n\n查看本机发布历史，不访问商店。\n',
    account: 'android-release account list [--json]\nandroid-release account update [渠道] [账号]\nandroid-release account forget [渠道] [账号]\n\n密钥经终端隐藏输入，本机安全复用。JSON 和 PEM 材料要求当前用户所有、权限 0600。\n',
    doctor: 'android-release doctor [--repair] [--json]\n\n检查工具自带组件。--repair 自动安装固定版本的 APK 校验组件，无需手动配置 Android SDK 或 JDK。\n',
    web: 'android-release web [--port 端口] [--no-open]\n\n启动仅监听 127.0.0.1 的本地 Web 端，与 CLI 共用应用、账号和发布记录。\n',
    protocols: 'android-release protocols [--json]\n\n查看渠道查询、核对能力和真实验证范围。\n',
    version: 'android-release version [--json]\n\n查看工具版本。\n',
  };
  return intro + (descriptions[context] ?? Object.values(descriptions).map(row => row.split('\n\n')[0]).join('\n\n') + '\n\n发布和恢复均通过 publish 进入。用任一命令的 --help 查看示例。\n');
}
function resultCode(release: ReleaseView, errors: Record<string, string>, selected?: Channel[]): number {
  if (release.recovery_required) return 3;
  const targets = selected ? release.targets.filter(row => selected.includes(row.channel as Channel)) : release.targets;
  if (targets.some(row => row.recovery_required)) return 3;
  if (targets.every(row => row.execution_phase === 'submitted')) return 0;
  return targets.some(row => row.execution_phase === 'submitted') ? 2 : Object.keys(errors).length ? 1 : 1;
}

export async function run(args: string[], injected: { application?: PublisherApplication; terminal?: TerminalIO; output?: (text: string) => void } = {}): Promise<number> {
  const out = injected.output ?? (text => process.stdout.write(text));
  const io = injected.terminal ?? new Terminal();
  let app = injected.application;
  const application = () => app ??= new PublisherApplication({ progress: message => io.write(message + '\n') });
  const json = (value: unknown) => out(JSON.stringify(value) + '\n');
  const errorsText = (errors: Record<string, string>) => { for (const [channel, message] of Object.entries(errors)) io.write(`${channel}：${message}\n`); };
  const chooseApp = async (id?: string): Promise<string> => {
    const apps = application().apps();
    if (id) { application().app(id); return id; }
    if (!apps.length) throw new Error('尚未配置应用。请运行 android-release app add');
    if (apps.length === 1) return apps[0]!.project_id;
    io.write(table([['应用', '短名', '包名', '渠道'], ...apps.map(row => [row.display_name ?? row.project_id, row.project_id, row.package_name ?? '', row.channels.map(target => channelNames[target.channel]).join('、')])]));
    const selected = await io.ask('输入应用短名'); application().app(selected); return selected;
  };
  try {
    const command = args[0] ?? '--help';
    if (command === 'help' || args.some(arg => ['--help', '-h'].includes(arg)) || !args.length) { out(help(args[1] && command === 'help' ? args[1] : command)); return 0; }
    const { positional, flags } = parse(args.slice(command === 'app' || command === 'account' ? 2 : 1));
    if (command !== 'account' && positional.length > 1) throw new Error('应用参数过多');
    if (positional.length && (['version', '--version', 'protocols', 'doctor', 'web'].includes(command) ||
      ['app', 'account'].includes(command) && args[1] === 'list')) throw new Error('此命令不接受应用参数');
    if (command === 'version' || command === '--version') {
      allow(flags, ['json']);
      const file = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'package.json');
      const version = JSON.parse(fs.readFileSync(file, 'utf8')).version as string;
      if (flags.json) json({ schema_version: 2, version, runtime: process.version, platform: process.platform, arch: process.arch }); else out(version + '\n');
      return 0;
    }
    if (command === 'protocols') { allow(flags, ['json']); if (flags.json) json({ channels: protocols() }); else out(protocolsText()); return 0; }
    if (command === 'doctor') {
      allow(flags, ['json', 'repair']);
      if (flags.repair) await ensureRuntime(injected.application?.root ?? dataRoot(), message => io.write(message + '\n'));
      const result = runtimeDiagnostic(injected.application?.root ?? dataRoot());
      if (flags.json) json(result);
      else out('发布环境检查\n' + (result.checks as Array<Record<string, unknown>>).map(row => `${row.status === 'ok' ? '[通过]' : '[待处理]'} ${row.name}${Array.isArray(row.fixes) ? '\n' + row.fixes.join('\n') : ''}`).join('\n') + '\n');
      return result.ready ? 0 : 1;
    }
    if (command === 'app') {
      const action = args[1];
      if (action === 'list') { allow(flags, ['json']); const apps = application().apps(); if (flags.json) json({ apps }); else out(apps.length ? table([['应用', '短名', '包名', '渠道'], ...apps.map(row => [row.display_name ?? row.project_id, row.project_id, row.package_name ?? '', row.channels.map(target => channelNames[target.channel]).join('、')])]) : '尚未配置应用。运行 android-release app add 开始。\n'); return 0; }
      if (!['add', 'edit'].includes(action ?? '')) throw new Error('使用 app add、app edit 或 app list');
      allow(flags, []);
      const id = action === 'edit' ? await chooseApp(positional[0]) : positional[0] ?? await io.ask('应用短名（供命令行选择，使用字母、数字、点、横线或下划线）');
      if (!validId(id)) throw new Error('应用配置名无效');
      const previous = application().apps().find(row => row.project_id === id);
      if (action === 'add' && previous) throw new Error('应用已存在，请使用 app edit');
      const project = await configureProject(id, io, previous);
      application().saveApp(project, action === 'edit');
      const prompt = new PromptCredentials();
      for (const row of project.channels) {
        if (application().vault.has(row.channel, row.account)) continue;
        io.write(`连接${channelNames[row.channel]}：凭据仅在此终端输入，验证后安全保存。\n`); io.suspend();
        try { await application().updateAccount(row.channel, row.account, await prompt.forChannel(row.channel)); }
        catch (error) {
          if (/canceled|取消/.test(publicError(error))) throw new Canceled();
          io.write(`${channelNames[row.channel]}：${publicError(error)}\n修复：android-release account update ${row.channel} ${row.account}\n`);
        }
      }
      prompt.clear();
      const checked = await application().checkApp(id); errorsText(checked.errors);
      out(`应用 ${id} 已保存。发布：android-release publish ${id}\n`);
      return Object.keys(checked.errors).length ? 1 : 0;
    }
    if (command === 'account') {
      const action = args[1];
      if (action === 'list') { allow(flags, ['json']); const accounts = application().accounts(); if (flags.json) json({ accounts }); else out(accounts.length ? table([['渠道', '账号备注', '凭据', '应用'], ...accounts.map(row => [channelNames[row.channel], row.account, row.saved ? '已保存' : '待配置', row.app_names.join('、') || '暂未关联应用'])]) : '尚无渠道账号。先运行 android-release app add。\n'); return 0; }
      allow(flags, []);
      if (!['update', 'forget'].includes(action ?? '') || positional.length > 2) throw new Error('使用 account list、account update 或 account forget');
      const channel = positional[0] ?? await io.ask('渠道', CHANNELS[0]);
      if (!isChannel(channel)) throw new Error('未知渠道');
      const accounts = application().accounts().filter(row => row.channel === channel);
      const account = positional[1] ?? (accounts.length === 1 ? accounts[0]!.account : await io.ask('账号备注'));
      const selected = accounts.find(row => row.account === account);
      if (!selected) throw new Error('账号尚未关联应用');
      if (action === 'forget') {
        io.write(`将清除 ${channelNames[channel]} / ${account} 的本机凭据。${selected.apps.length ? '受影响应用：' + selected.app_names.join('、') : '目前没有应用关联'}。发布回执和未决请求记录会保留。\n`);
        if (await io.ask('输入 yes 清除') !== 'yes') throw new Canceled();
        application().forgetAccount(channel, account); out('本机凭据已清除。\n'); return 0;
      }
      if (!selected.apps.length) throw new Error('这个账号暂未关联应用。请先在应用资料中选择此渠道账号，再验证和更新凭据。');
      io.suspend(); const prompt = new PromptCredentials();
      try { await application().updateAccount(channel, account, await prompt.forChannel(channel)); }
      finally { prompt.clear(); }
      out('凭据已验证并安全保存。\n'); return 0;
    }
    if (command === 'web') {
      allow(flags, ['port', 'no-open']);
      const port = flags.port === undefined ? 0 : Number(flags.port);
      if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('端口无效');
      const server = await startWebServer(application(), { port, root: application().root });
      out(`本地 Web 端：${server.url}\n仅本机可访问；按 Ctrl+C 停止。\n`);
      if (!flags['no-open']) spawn('/usr/bin/open', [server.url], { stdio: 'ignore' }).on('error', () => {});
      await new Promise<void>(resolve => { process.once('SIGINT', resolve); process.once('SIGTERM', resolve); });
      await server.close(); return 0;
    }
    if (command === 'history') {
      allow(flags, ['json']); const id = await chooseApp(positional[0]); const releases = application().releases(id);
      if (flags.json) json({ releases });
      else out(releases.length ? table([['发布记录', '版本', '受理', '创建时间'], ...releases.map(row => [row.batch_id, `${row.version_name}（${row.version_code}）`, `${row.accepted_targets}/${row.total_targets}`, row.created_at])]) : '此应用尚无发布记录。\n');
      return 0;
    }
    if (command === 'status') {
      allow(flags, ['json', 'cached', 'release', 'record-manual', 'reconcile']);
      if (flags.reconcile && (flags.cached || flags['record-manual'])) throw new Error('--reconcile 与 --cached、--record-manual 须分别使用');
      if (flags.json && flags['record-manual']) throw new Error('人工核对需要终端交互');
      const id = await chooseApp(positional[0]); const releases = application().releases(id);
      const release = flags.release ? application().release(String(flags.release)) : releases[0];
      if (!release || release.project_id !== id) throw new Error('没有对应的发布记录');
      if (flags['record-manual']) {
        io.write(releaseText(release));
        const ch = await io.ask('核对渠道'); if (!isChannel(ch)) throw new Error('未知渠道');
        const review = await io.ask('审核结果 unknown/in_review/approved/rejected', 'unknown');
        const availability = await io.ask('上架结果 unknown/live/not_live', 'unknown');
        const source = await io.ask('核对来源（例如商店后台的版本记录）');
        const observed = await io.ask('核对时间（ISO 时间）', new Date().toISOString());
        const note = await io.ask('备注（可选）');
        if (await io.ask(`确认核对的是 ${release.package_name} / ${release.version_name}（${release.version_code}），输入 yes 保存`) !== 'yes') throw new Canceled();
        const saved = application().recordManual(release.batch_id, { channel: ch, review_state: review as 'unknown',
          availability_state: availability as 'unknown', source, observed_at: observed, note });
        out(releaseText(saved)); return 0;
      }
      const result = flags.reconcile ? await application().reconcileRelease(release.batch_id)
        : await application().getRelease(release.batch_id, !flags.cached);
      if (flags.json) json(result); else { out(releaseText(result.release)); errorsText(result.errors); }
      return flags.reconcile && result.release.recovery_required ? 3 : Object.keys(result.errors).length ? 1 : 0;
    }
    if (command === 'publish') {
      allow(flags, ['apk', 'channels', 'notes-file']);
      const id = await chooseApp(positional[0]);
      const config = application().app(id);
      let selected = selection(flags.channels) ?? config.channels.map(row => row.channel);
      if (selected.some(ch => !config.channels.some(row => row.channel === ch))) throw new Error('所选渠道尚未配置');
      let release: ReleaseView | undefined;
      const pending = application().releases(id).find(row => !row.all_accepted);
      if (pending && !flags.apk && !flags['notes-file']) {
        io.write(releaseText(pending));
        const action = await io.ask('继续已有发布 / 准备新版本 / 查看结果：输入 continue/new/status', 'continue');
        if (action === 'status') { out(releaseText(pending)); return 0; }
        if (action === 'continue') {
          release = pending;
          const available = pending.targets.filter(row => row.execution_phase !== 'submitted' && !row.recovery_required).map(row => row.channel as Channel);
          selected = flags.channels ? selected : available;
          if (!selected.length || selected.some(channel => !available.includes(channel))) {
            out(releaseText(pending));
            if (pending.recovery_required) return 3;
            throw new Error('所选渠道目前无法重试，请查看发布状态');
          }
        }
        else if (action !== 'new') throw new Canceled();
      }
      if (!release) {
        const apks: Partial<Record<Channel, string>> = {};
        for (const entry of flags.apk as string[] ?? []) {
          const sep = entry.indexOf('='); const channel = entry.slice(0, sep);
          if (sep < 1 || !isChannel(channel) || apks[channel] || !selected.includes(channel)) throw new Error('--apk 需要唯一的 渠道=文件路径');
          apks[channel] = path.resolve(entry.slice(sep + 1));
        }
        const skipped: Channel[] = [];
        for (const ch of selected) if (!apks[ch]) {
          const file = await io.ask(`${channelNames[ch]} APK 路径（输入 skip 跳过）`);
          if (file === 'skip') skipped.push(ch); else if (file) apks[ch] = path.resolve(file); else throw new Error(`${ch}：需要 APK 路径`);
        }
        if (skipped.length) {
          io.write('将跳过：' + skipped.map(ch => channelNames[ch]).join('、') + '\n');
          if (await io.ask('输入 yes 确认缩小本次范围') !== 'yes') throw new Canceled();
          selected = selected.filter(ch => !skipped.includes(ch));
        }
        const notes = typeof flags['notes-file'] === 'string' ? new TextDecoder('utf-8', { fatal: true }).decode(regularFile(path.resolve(flags['notes-file']), 65536))
          : await io.ask('更新说明：输入 text 粘贴，或输入文件路径', 'text').then(value => value === 'text' ? io.multiline() : new TextDecoder('utf-8', { fatal: true }).decode(regularFile(path.resolve(value), 65536)));
        const prepared = await application().prepareRelease({ app_id: id, apks, release_notes: notes, channels: selected });
        release = prepared.release;
        if (prepared.duplicate) io.write('已找到相同内容的发布，回到原记录。\n');
        if (prepared.related_release) io.write(`这组 APK 已有记录 ${prepared.related_release}；本次说明或配置有变更，将使用新的发布内容。\n`);
      }
      if (release.all_accepted) { out(releaseText(release)); return 0; }
      let checked = await application().preflightRelease(release.batch_id, selected);
      while (Object.keys(checked.errors).length) {
        errorsText(checked.errors);
        const action = await io.ask('修复后重查 retry；明确跳过失败渠道 skip；或 cancel', 'retry');
        if (action === 'retry') checked = await application().preflightRelease(release.batch_id, selected);
        else if (action === 'skip') {
          const excluded = Object.keys(checked.errors);
          if (await io.ask(`将跳过 ${excluded.join('、')}，输入 yes 确认`) !== 'yes') throw new Canceled();
          selected = selected.filter(ch => !excluded.includes(ch));
          if (!selected.length) throw new Error('所有渠道均未就绪，未提交');
          checked = { release: checked.release, errors: {} };
        } else throw new Canceled();
      }
      const attemptOnly = release.has_writes === true;
      const reviewed = application().reviewRelease(release.batch_id, selected, attemptOnly);
      if (attemptOnly) io.write('本次只处理所选未完成渠道；原发布的其他渠道和结果继续保留。\n');
      io.write(`\n应用：${config.display_name ?? id} / ${release.package_name}\n版本：${release.version_name}（${release.version_code}）\n审核通过后立即上架。\n`);
      for (const target of reviewed.release.targets.filter(row => selected.includes(row.channel as Channel))) {
        const artifact = target.artifact as Record<string, unknown>;
        io.write(`${channelNames[target.channel as Channel]} → ${artifact.filename}；商店应用 ${target.app_id}；账号 ${target.account}\n`);
      }
      io.write(`\n更新说明：\n${reviewed.plan.release_notes}\n`);
      if (await io.ask('确认以上应用、APK 和渠道范围，输入 yes 上传并提交') !== 'yes') throw new Canceled();
      const result = await application().submitRelease(release.batch_id, reviewed.plan.approval_plan, attemptOnly);
      out(releaseText(result.release)); errorsText(result.errors);
      return resultCode(result.release, result.errors, selected);
    }
    throw new Error('未知命令。运行 android-release --help 查看用法');
  } finally { io.close(); if (!injected.application) app?.close(); }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv[2] !== 'web') process.once('SIGINT', () => {
    if (process.stdin.isTTY) process.stdin.setRawMode(false);
    process.stderr.write('\n已中断，已有发布记录保留。\n');
    process.exit(130);
  });
  if (process.env.NODE_USE_ENV_PROXY !== '0' && (process.env.HTTPS_PROXY || process.env.https_proxy || process.env.HTTP_PROXY || process.env.http_proxy)) setGlobalProxyFromEnv();
  run(process.argv.slice(2)).then(code => { process.exitCode = code; }).catch(error => {
    const message = publicError(error);
    if (process.argv.includes('--json')) process.stdout.write(JSON.stringify({ error: message }) + '\n');
    else process.stderr.write(message + '\n');
    process.exitCode = error instanceof Canceled || /canceled|取消/.test(message) ? 130 : error instanceof Error && error.name === 'RecoveryRequired' ? 3 : 1;
  });
}
