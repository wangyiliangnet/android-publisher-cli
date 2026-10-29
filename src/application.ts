import fs from 'node:fs';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { type ArtifactInspector } from './artifact.js';
import { channelAdapter } from './channels/index.js';
import { protocols } from './channels/capabilities.js';
import { type CredentialSet, type CredentialSource } from './credentials.js';
import { loadProject, prepare, preflight, previewBatch, publicError, reconcile, refresh, review, saveProject, submit, validateSubmission } from './engine.js';
import { type Approval, type Batch, type Channel, type Project, type ProjectChannel, canonical, isChannel, validHash, validId, validPackage, validateProject } from './model.js';
import { dataRoot, hashFile, privateDir, readJSON, writePrivate } from './private-file.js';
import { ensureRuntime, runtimeDiagnostic } from './runtime.js';
import { type ManualObservation, Store } from './state.js';
import { CredentialVault } from './vault.js';

export interface ReleaseView extends Record<string, unknown> {
  batch_id: string;
  project_id: string;
  package_name: string;
  version_name: string;
  version_code: string;
  created_at: string;
  release_notes: string;
  targets: Array<Record<string, unknown>>;
}
export interface PrepareReleaseInput {
  app_id: string;
  apks: Partial<Record<Channel, string>>;
  release_notes: string;
  channels?: Channel[];
}
export interface ApplicationOptions {
  root?: string;
  store?: Store;
  tools?: ArtifactInspector | (() => Promise<ArtifactInspector>);
  vault?: CredentialVault;
  credentials?: CredentialSource;
  fetcher?: typeof fetch;
  progress?: (message: string) => void;
}
export class RecoveryRequired extends Error {
  constructor(message: string) { super(message); this.name = 'RecoveryRequired'; }
}

/** Both frontends use this boundary. Only submitRelease can issue business writes. */
export class PublisherApplication {
  readonly store: Store;
  readonly root: string;
  readonly vault: CredentialVault;
  private readonly fetcher: typeof fetch;
  private readonly options: ApplicationOptions;
  constructor(options: ApplicationOptions = {}) {
    this.options = options;
    this.root = options.root ?? options.store?.root ?? dataRoot();
    this.store = options.store ?? new Store(this.root);
    this.vault = options.vault ?? new CredentialVault(this.root);
    this.fetcher = options.fetcher ?? fetch;
  }
  close(): void { this.store.close(); }
  private async tools(): Promise<ArtifactInspector> {
    return typeof this.options.tools === 'function' ? this.options.tools()
      : this.options.tools ?? ensureRuntime(this.root, this.options.progress);
  }
  private source(targets: Array<{ channel: Channel; account: string }>): CredentialSource {
    return this.options.credentials ?? this.vault.source(targets);
  }
  apps(): Project[] {
    const directory = path.join(this.root, 'projects');
    if (!fs.existsSync(directory)) return [];
    return fs.readdirSync(directory).filter(name => name.endsWith('.json')).sort()
      .map(name => loadProject(name.slice(0, -5), this.root));
  }
  saveApp(value: unknown, replace = false): Project {
    const app = validateProject(value);
    if (!validPackage(app.package_name)) throw new Error('请填写应用包名，例如 com.example.app');
    for (const row of app.channels) if (['xiaomi', 'oppo', 'vivo'].includes(row.channel) && row.app_id !== app.package_name)
      throw new Error(`${row.channel}：渠道包名与应用包名不一致`);
    for (const row of app.channels) for (const other of this.apps()) for (const saved of other.channels) {
      if (row.channel === saved.channel && row.client_id && row.client_id === saved.client_id && row.account !== saved.account)
        throw new Error(`${row.channel}：此 API 账号已使用备注 ${saved.account}，请复用该备注`);
    }
    saveProject(app, this.root, replace);
    return app;
  }
  app(id: string): Project { return loadProject(id, this.root); }
  releases(appId: string): ReleaseView[] {
    this.app(appId);
    return this.store.batchIds().filter(id => this.store.loadBatch(id).project_id === appId).map(id => this.release(id));
  }
  release(id: string): ReleaseView {
    const batch = this.store.loadBatch(id);
    const summary = this.store.summary(id);
    const selected = this.selection(id);
    const targets = (summary.targets as Array<Record<string, unknown>>).filter(row => selected.includes(row.channel as Channel)).map<Record<string, unknown>>(row => {
      const frozen = batch.targets.find(target => target.channel === row.channel)!;
      const recovery = (row.recovery as Array<Record<string, unknown>>).map(issue => ({ ...issue,
        next_action: `android-release status ${batch.project_id} --release ${String(issue.batch_id ?? id)}` }));
      return { ...row, recovery, account: frozen.account, app_id: frozen.app_id, artifact: frozen.artifact,
        manual_observations: this.store.manualObservations(id, frozen.channel) };
    });
    const accepted = targets.filter(row => row.execution_phase === 'submitted').length;
    const approved = targets.filter(row => row.review_state === 'approved').length;
    const live = targets.filter(row => row.availability_state === 'live').length;
    return { ...summary, total_targets: targets.length, accepted_targets: accepted, approved_targets: approved, live_targets: live,
      all_accepted: accepted === targets.length, all_approved: approved === targets.length, all_live: live === targets.length,
      recovery_required: targets.some(row => row.recovery_required), selected_channels: selected,
      excluded_channels: batch.targets.map(target => target.channel).filter(channel => !selected.includes(channel)),
      batch_id: id, project_id: batch.project_id, package_name: batch.package_name,
      version_name: batch.version_name, version_code: batch.version_code, created_at: batch.created_at,
      release_notes: batch.release_notes, release_policy: batch.release_policy, targets,
      manual_observations: this.store.manualObservations(id) };
  }
  private selection(id: string): Channel[] {
    const batch = this.store.loadBatch(id);
    const file = path.join(this.root, 'release-scopes', `${batch.batch_id}.json`);
    if (!fs.existsSync(file)) return batch.targets.map(target => target.channel);
    const selected = (readJSON(file, 4096, true) as { selected_channels: Channel[] }).selected_channels;
    if (!Array.isArray(selected) || !selected.length || new Set(selected).size !== selected.length ||
      selected.some(channel => !batch.targets.some(target => target.channel === channel))) throw new Error('invalid saved release scope');
    return selected;
  }
  async checkChannel(app: Project, row: ProjectChannel, credentials?: CredentialSet): Promise<void> {
    const artifact = { path: '/connection-check.apk', filename: 'connection-check.apk', sha256: '0'.repeat(64),
      signer_sha256: '0'.repeat(64), size: 1, package_name: app.package_name ?? row.app_id,
      version_name: 'connection-check', version_code: '9223372036854775807', abis: ['arm64-v8a'] };
    const target = { ...row, artifact };
    const batch: Batch = { schema_version: 2, batch_id: `connection-${randomBytes(8).toString('hex')}`,
      project_id: app.project_id, package_name: artifact.package_name, version_name: artifact.version_name,
      version_code: artifact.version_code, signer_sha256: artifact.signer_sha256, release_notes: '检查渠道连接权限',
      release_policy: 'after_approval', targets: [target], digest: '0'.repeat(64), created_at: new Date().toISOString() };
    const secrets = credentials ?? await this.source([row]).forChannel(row.channel);
    await channelAdapter(batch, target, secrets, this.store, this.fetcher).preflight();
  }
  async checkApp(id: string): Promise<{ channels: Array<{ channel: Channel; connected: boolean }>; errors: Record<string, string> }> {
    const app = this.app(id);
    const errors: Record<string, string> = {};
    const channels = [];
    for (const row of app.channels) {
      try { await this.checkChannel(app, row); channels.push({ channel: row.channel, connected: true }); }
      catch (error) { errors[row.channel] = publicError(error); channels.push({ channel: row.channel, connected: false }); }
    }
    writePrivate(path.join(this.root, 'connections', `${id}.json`), JSON.stringify({ checked_at: new Date().toISOString(), channels, errors }) + '\n', true, true);
    return { channels, errors };
  }
  async updateAccount(channel: Channel, account: string, credentials: CredentialSet): Promise<void> {
    const apps = this.apps().filter(app => app.channels.some(row => row.channel === channel && row.account === account));
    if (!apps.length) throw new Error('账号尚未关联应用，请先运行 android-release app add');
    for (const clientId of new Set(apps.map(app => {
      const row = app.channels.find(row => row.channel === channel && row.account === account)!;
      return row.client_id ?? row.account;
    }))) this.store.invalidateAccountToken(channel, account, clientId);
    for (const app of apps) await this.checkChannel(app, app.channels.find(row => row.channel === channel && row.account === account)!, credentials);
    this.vault.save(channel, account, credentials);
  }
  accounts(): Array<{ channel: Channel; account: string; apps: string[]; saved: boolean }> {
    const result = new Map<string, { channel: Channel; account: string; apps: string[]; saved: boolean }>();
    for (const app of this.apps()) for (const row of app.channels) {
      const key = `${row.channel}:${row.account}`;
      const previous = result.get(key);
      if (previous) previous.apps.push(app.project_id);
      else result.set(key, { channel: row.channel, account: row.account, apps: [app.project_id], saved: this.vault.has(row.channel, row.account) });
    }
    return [...result.values()];
  }
  forgetAccount(channel: Channel, account: string): void {
    if (!isChannel(channel) || !validId(account)) throw new Error('invalid account');
    this.vault.forget(channel, account);
    const clientIds = this.apps().flatMap(app => app.channels.filter(row => row.channel === channel && row.account === account).map(row => row.client_id ?? row.account));
    this.store.forgetAccountMaterials(channel, account, clientIds);
  }
  async prepareRelease(input: PrepareReleaseInput): Promise<{ release: ReleaseView; duplicate: boolean; related_release?: string }> {
    const app = this.app(input.app_id);
    const selected = input.channels ?? app.channels.map(row => row.channel);
    if (!Array.isArray(selected) || !selected.length || new Set(selected).size !== selected.length ||
      selected.some(channel => !isChannel(channel) || !app.channels.some(row => row.channel === channel))) throw new Error('请选择已配置的渠道');
    if (!input.apks || typeof input.apks !== 'object' || Array.isArray(input.apks) ||
      Object.keys(input.apks).some(channel => !selected.includes(channel as Channel))) throw new Error('APK 对应关系包含未选择的渠道');
    for (const channel of selected) if (!input.apks[channel]) throw new Error(`${channel}：请选择本渠道的 APK`);
    if (typeof input.release_notes !== 'string' || Buffer.byteLength(input.release_notes) > 65536) throw new Error('更新说明无效');
    const tools = await this.tools();
    const id = `${app.project_id.slice(0, 90)}-${Date.now().toString(36)}-${randomBytes(5).toString('hex')}`;
    const notes = path.join(this.root, 'drafts', `${id}.txt`);
    writePrivate(notes, input.release_notes, false, true);
    try {
      const batchInput = { schema_version: 2 as const, batch_id: id, project_id: app.project_id, release_notes_file: notes,
        apks: Object.fromEntries(selected.map(channel => [channel, input.apks[channel]!])) };
      const preview = previewBatch(batchInput, tools, this.store);
      if (app.package_name && app.package_name !== preview.package_name) throw new Error('APK 包名与应用配置不一致');
      let related: string | undefined;
      for (const previous of this.releases(app.project_id)) {
        const old = this.store.loadBatch(previous.batch_id);
        const sameArtifacts = old.targets.length === selected.length && old.targets.every(target =>
          preview.apk_sha256[target.channel] === target.artifact.sha256);
        if (!sameArtifacts) continue;
        related ??= old.batch_id;
        const sameBindings = old.targets.every(target => {
          const config = app.channels.find(row => row.channel === target.channel)!;
          return target.account === config.account && target.app_id === config.app_id &&
            target.client_id === config.client_id && target.language_id === config.language_id &&
            target.privacy_url === config.privacy_url && (!config.icon_path || hashFile(config.icon_path).sha256 === target.icon_sha256);
        });
        const oldSelection = this.selection(old.batch_id);
        if (sameBindings && old.release_notes === input.release_notes && oldSelection.length === selected.length &&
          selected.every(channel => oldSelection.includes(channel))) return { release: previous, duplicate: true };
      }
      prepare(batchInput, tools, this.store, preview);
      return { release: this.release(id), duplicate: false, ...(related ? { related_release: related } : {}) };
    } finally { fs.unlinkSync(notes); }
  }
  async preflightRelease(id: string, channels?: Channel[]): Promise<{ release: ReleaseView; errors: Record<string, string> }> {
    const batch = this.store.loadBatch(id);
    const result = await preflight(this.store, id, this.source(batch.targets), channels ?? this.selection(id), this.fetcher);
    return { release: this.release(id), errors: result.errors as Record<string, string> };
  }
  reviewRelease(id: string, channels?: Channel[]): { release: ReleaseView; plan: Approval } {
    const batch = this.store.loadBatch(id);
    const selected = channels ?? this.selection(id);
    for (const target of batch.targets) if (!selected.includes(target.channel) && this.store.operations(id, target.channel).length)
      throw new Error(`${target.channel}：已有执行记录的渠道须保留在本次发布中`);
    for (const channel of selected) if (this.store.recovery(id, channel).length) throw new RecoveryRequired(`${channel}：请求结果待核对，请先查看发布状态`);
    const directory = path.join(this.root, 'plans'); privateDir(directory);
    const temp = path.join(directory, `${id}-${randomBytes(8).toString('hex')}.json`);
    const plan = review(this.store, id, temp, selected);
    fs.renameSync(temp, this.planFile(id, plan.approval_plan));
    return { release: this.release(id), plan };
  }
  private planFile(id: string, hash: string): string {
    if (!validId(id) || !validHash(hash)) throw new Error('invalid submission confirmation');
    return path.join(this.root, 'plans', `${id}-${hash}.json`);
  }
  async submitRelease(id: string, hash: string): Promise<{ release: ReleaseView; errors: Record<string, string> }> {
    const batch = this.store.loadBatch(id);
    const file = this.planFile(id, hash);
    const plan = validateSubmission(this.store, id, file, hash);
    for (const target of batch.targets) if (!plan.selected_channels.includes(target.channel) && this.store.operations(id, target.channel).length)
      throw new RecoveryRequired(`${target.channel}：已有执行记录，无法用旧计划缩小范围。请重新查看并核对本次发布`);
    writePrivate(path.join(this.root, 'release-scopes', `${id}.json`), JSON.stringify({ selected_channels: plan.selected_channels }) + '\n', true, true);
    const result = await submit(this.store, id, file, hash, this.source(batch.targets), await this.tools(), true, this.fetcher);
    return { release: this.release(id), errors: result.errors as Record<string, string> };
  }
  async getRelease(id: string, update = false): Promise<{ release: ReleaseView; errors: Record<string, string> }> {
    const batch = this.store.loadBatch(id);
    const result = update ? await refresh(this.store, id, this.source(batch.targets), this.selection(id), this.fetcher) : { errors: {} };
    return { release: this.release(id), errors: result.errors as Record<string, string> };
  }
  async reconcileRelease(id: string, channels?: Channel[]): Promise<{ release: ReleaseView; errors: Record<string, string> }> {
    const batch = this.store.loadBatch(id);
    const result = await reconcile(this.store, id, this.source(batch.targets), channels ?? this.selection(id), this.fetcher);
    return { release: this.release(id), errors: result.errors as Record<string, string> };
  }
  recordManual(id: string, input: Pick<ManualObservation, 'channel' | 'source' | 'observed_at'> & Partial<Pick<ManualObservation, 'review_state' | 'availability_state' | 'note'>>): ReleaseView {
    this.store.recordManual(id, { ...input, review_state: input.review_state ?? 'unknown',
      availability_state: input.availability_state ?? 'unknown', note: input.note ?? '' });
    return this.release(id);
  }
  async invoke(action: string, payload: unknown): Promise<unknown> {
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('expected action payload');
    const p = payload as Record<string, unknown>;
    const text = (key: string): string => { if (typeof p[key] !== 'string' || !p[key]) throw new Error(`missing ${key}`); return p[key]; };
    const channels = (): Channel[] | undefined => {
      if (p.channels === undefined) return undefined;
      if (!Array.isArray(p.channels) || !p.channels.length || p.channels.some(ch => !isChannel(ch))) throw new Error('invalid channels');
      return p.channels as Channel[];
    };
    switch (action) {
      case 'apps.list': return { apps: this.apps() };
      case 'apps.save': return { app: this.saveApp(p.app, p.replace === true) };
      case 'apps.check': return this.checkApp(text('app_id'));
      case 'releases.list': return { releases: this.releases(text('app_id')) };
      case 'releases.prepare': return this.prepareRelease({ app_id: text('app_id'), apks: p.apks as PrepareReleaseInput['apks'],
        release_notes: text('release_notes'), ...(p.channels === undefined ? {} : { channels: channels()! }) });
      case 'releases.preflight': return this.preflightRelease(text('release_id'), channels());
      case 'releases.review': return this.reviewRelease(text('release_id'), channels());
      case 'releases.submit': return this.submitRelease(text('release_id'), text('approval_plan'));
      case 'releases.get': return this.getRelease(text('release_id'), p.refresh === true);
      case 'releases.reconcile': return this.reconcileRelease(text('release_id'), channels());
      case 'releases.manual': return { release: this.recordManual(text('release_id'), {
        channel: text('channel') as Channel, source: text('source'), observed_at: text('observed_at'),
        review_state: (p.review_state ?? 'unknown') as ManualObservation['review_state'],
        availability_state: (p.availability_state ?? 'unknown') as ManualObservation['availability_state'],
        note: typeof p.note === 'string' ? p.note : '',
      }) };
      case 'accounts.list': return { accounts: this.accounts() };
      case 'accounts.forget': this.forgetAccount(text('channel') as Channel, text('account')); return { forgotten: true };
      case 'doctor': return runtimeDiagnostic(this.root);
      case 'protocols': return { channels: protocols() };
      default: throw new Error('unsupported application action');
    }
  }
}
