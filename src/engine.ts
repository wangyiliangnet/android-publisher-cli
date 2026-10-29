import fs from 'node:fs';
import path from 'node:path';
import { type ArtifactInspector, inspectAndFreeze, verifyFrozen } from './artifact.js';
import { channelAdapter } from './channels/index.js';
import { validateChannelArtifact, validateReleaseNotes } from './channels/capabilities.js';
import { ChannelError } from './channels/http.js';
import { type ChannelAdapter } from './channels/types.js';
import { type CredentialSource } from './credentials.js';
import { type Approval, type Batch, type BatchInput, type Channel, type FrozenTarget, type Preflight,
  type Project, canonical, digest, isChannel, parseStrictJSON, validAbsolute, validId, validateBatchInput,
  validateProject } from './model.js';
import { dataRoot, freezeFile, hashFile, privateDir, readJSON, regularFile, writePrivate } from './private-file.js';
import { Store } from './state.js';

export function projectPath(root: string, id: string): string {
  if (!validId(id)) throw new Error('invalid project ID');
  return path.join(root, 'projects', `${id}.json`);
}
export function saveProject(project: Project, root = dataRoot(), replace = false): void {
  validateProject(project);
  writePrivate(projectPath(root, project.project_id), JSON.stringify(project, null, 2) + '\n', replace, true);
}
export function loadProject(id: string, root = dataRoot()): Project {
  return validateProject(readJSON(projectPath(root, id), 64 * 1024, true));
}

export interface BatchPreview {
  package_name: string;
  version_name: string;
  version_code: string;
  signer_sha256: string;
  apk_sha256: Partial<Record<Channel, string>>;
}

function batchInputs(input: BatchInput, store: Store): { project: Project; notes: string } {
  validateBatchInput(input);
  const configured = loadProject(input.project_id, store.root);
  const selected = Object.keys(input.apks);
  if (!selected.length || selected.some(channel => !configured.channels.some(row => row.channel === channel)))
    throw new Error('APK selection contains an unconfigured channel');
  const project = { ...configured, channels: configured.channels.filter(row => selected.includes(row.channel)) };
  const notes = new TextDecoder('utf-8', { fatal: true }).decode(regularFile(input.release_notes_file, 64 * 1024));
  validateReleaseNotes(notes, project.channels.map(target => target.channel));
  for (const target of project.channels) {
    if (target.channel === 'xiaomi') {
      try { hashFile(target.icon_path!); }
      catch { throw new Error('小米（xiaomi）图标不可读取，请检查应用配置中的 icon_path 后重新 prepare'); }
    }
  }
  return { project, notes };
}

function checkPackageAppId(channel: Channel, appId: string, packageName: string): void {
  if (['xiaomi', 'oppo', 'vivo'].includes(channel) && appId !== packageName)
    throw new Error(channel + ': configured app ID differs from APK package');
}

export function previewBatch(input: BatchInput, tools: ArtifactInspector, store: Store): BatchPreview {
  const { project } = batchInputs(input, store);
  let packageName = ''; let versionName = ''; let versionCode = ''; let signer = '';
  const apkHashes: Partial<Record<Channel, string>> = {};
  for (const config of project.channels) {
    const apkPath = input.apks[config.channel]!;
    const before = hashFile(apkPath);
    const inspected = tools.inspect(apkPath);
    const after = hashFile(apkPath);
    if (before.sha256 !== after.sha256 || before.size !== after.size) throw new Error('APK changed while being inspected');
    if (!packageName) {
      packageName = inspected.package_name; versionName = inspected.version_name;
      versionCode = inspected.version_code; signer = inspected.signer_sha256;
    } else if (packageName !== inspected.package_name || versionName !== inspected.version_name ||
               versionCode !== inspected.version_code || signer !== inspected.signer_sha256) {
      throw new Error('all APKs must have the same package, version and signer');
    }
    checkPackageAppId(config.channel, config.app_id, inspected.package_name);
    validateChannelArtifact(config.channel, inspected);
    apkHashes[config.channel] = before.sha256;
  }
  return { package_name: packageName, version_name: versionName, version_code: versionCode,
    signer_sha256: signer, apk_sha256: apkHashes };
}

export function prepare(input: BatchInput, tools: ArtifactInspector, store: Store, expected: BatchPreview): Batch {
  const { project, notes } = batchInputs(input, store);
  const targets: FrozenTarget[] = [];
  let packageName = ''; let versionName = ''; let versionCode = ''; let signer = '';
  for (const config of project.channels) {
    const apkPath = input.apks[config.channel];
    if (!apkPath) throw new Error(`${config.channel}: APK path missing`);
    const artifact = inspectAndFreeze(tools, apkPath, store.root);
    if (artifact.sha256 !== expected.apk_sha256[config.channel]) throw new Error('APK changed after confirmation');
    if (!packageName) {
      packageName = artifact.package_name; versionName = artifact.version_name;
      versionCode = artifact.version_code; signer = artifact.signer_sha256;
    } else if (packageName !== artifact.package_name || versionName !== artifact.version_name ||
               versionCode !== artifact.version_code || signer !== artifact.signer_sha256) {
      throw new Error('all APKs must have the same package, version and signer');
    }
    checkPackageAppId(config.channel, config.app_id, artifact.package_name);
    validateChannelArtifact(config.channel, artifact);
    const target: FrozenTarget = { channel: config.channel, account: config.account, app_id: config.app_id,
      artifact, ...(config.client_id ? { client_id: config.client_id } : {}),
      ...(config.language_id ? { language_id: config.language_id } : {}),
      ...(config.privacy_url ? { privacy_url: config.privacy_url } : {}) };
    if (config.channel === 'xiaomi') {
      const iconSource = config.icon_path!;
      const iconHash = hashFile(iconSource);
      const frozenIcon = path.join(store.root, 'artifacts', iconHash.sha256, `icon${path.extname(iconSource) || '.png'}`);
      freezeFile(iconSource, frozenIcon, iconHash.sha256);
      target.icon_path = frozenIcon; target.icon_sha256 = iconHash.sha256; target.icon_size = iconHash.size;
    }
    targets.push(target);
  }
  if (expected.package_name !== packageName || expected.version_name !== versionName ||
      expected.version_code !== versionCode || expected.signer_sha256 !== signer)
    throw new Error('APK identity changed after confirmation');
  const body = { schema_version: 2 as const, batch_id: input.batch_id, project_id: project.project_id,
    package_name: packageName, version_name: versionName, version_code: versionCode,
    signer_sha256: signer, release_notes: notes, release_policy: 'after_approval' as const, targets };
  const batch: Batch = { ...body, digest: digest(body), created_at: new Date().toISOString() };
  store.saveBatch(batch);
  return batch;
}

function selectedTargets(batch: Batch, channels?: Channel[]): FrozenTarget[] {
  if (!channels?.length) return batch.targets;
  if (new Set(channels).size !== channels.length) throw new Error('duplicate channel selection');
  const rows = channels.map(channel => batch.targets.find(target => target.channel === channel));
  if (rows.some(row => !row)) throw new Error('selected channel is outside the immutable batch');
  return rows as FrozenTarget[];
}

export async function preflight(store: Store, batchId: string, credentials: CredentialSource,
  channels?: Channel[], fetcher: typeof fetch = fetch): Promise<Record<string, unknown>> {
  const batch = store.loadBatch(batchId);
  const errors: Record<string, string> = {};
  for (const target of selectedTargets(batch, channels)) {
    try {
      const row = store.target(batchId, target.channel);
      if (row.phase === 'submitted' || store.operations(batchId, target.channel).length) continue;
      const secrets = await credentials.forChannel(target.channel);
      const adapter = channelAdapter(batch, target, secrets, store, fetcher);
      const result = await adapter.preflight();
      store.savePreflight(batchId, result);
    } catch (error) { errors[target.channel] = publicError(error); }
  }
  return { ...store.summary(batchId), effect: 'read_only', errors };
}

function approvalBody(store: Store, batch: Batch, channels?: Channel[], requireReady = false): Omit<Approval, 'approval_plan'> {
  const selected = selectedTargets(batch, channels);
  const preflightDigests: Record<string, string> = {};
  const hashes: Record<string, string> = {};
  for (const target of selected) {
    const row = store.target(batch.batch_id, target.channel);
    if (!row.preflight_digest || requireReady && !['preflighted', 'submitted'].includes(row.phase)) throw new Error(`${target.channel}: a clean preflight is required`);
    if (requireReady && store.operations(batch.batch_id, target.channel).some(op => ['intent', 'unknown'].includes(op.effect))) throw new Error(`${target.channel}: unresolved write blocks review`);
    preflightDigests[target.channel] = row.preflight_digest;
    hashes[target.channel] = target.artifact.sha256;
  }
  return { schema_version: 2, batch_id: batch.batch_id, batch_digest: batch.digest,
    selected_channels: selected.map(target => target.channel),
    target_accounts: selected.map(target => ({ channel: target.channel, account: target.account,
      app_id: target.app_id, ...(target.client_id ? { client_id: target.client_id } : {}) })),
    preflight_digests: preflightDigests,
    package_name: batch.package_name, version_name: batch.version_name, version_code: batch.version_code,
    artifact_sha256: hashes, signer_sha256: batch.signer_sha256, release_notes: batch.release_notes,
    release_policy: batch.release_policy };
}

export function review(store: Store, batchId: string, file: string, channels?: Channel[]): Approval {
  if (!validAbsolute(file)) throw new Error('approval file path must be absolute');
  const body = approvalBody(store, store.loadBatch(batchId), channels, true);
  const approval = { ...body, approval_plan: digest(body) };
  writePrivate(file, JSON.stringify(approval, null, 2) + '\n');
  return approval;
}

function validatedApproval(store: Store, batch: Batch, file: string, approvedPlan: string): Approval {
  const raw = readJSON(file, 8 * 1024 * 1024, true);
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new Error('approval file is invalid');
  const approval = raw as Approval;
  if (approval.schema_version !== 2 || approval.batch_id !== batch.batch_id ||
    approval.approval_plan !== approvedPlan || !Array.isArray(approval.selected_channels) ||
    approval.selected_channels.some(ch => !isChannel(ch))) throw new Error('approval does not match the requested plan');
  const body = approvalBody(store, batch, approval.selected_channels);
  const expected = { ...body, approval_plan: digest(body) };
  if (canonical(approval) !== canonical(expected)) throw new Error('approval file or reviewed inputs changed');
  store.verifyApproval(approval);
  return approval;
}

export function validateSubmission(store: Store, batchId: string, file: string, approvedPlan: string): Approval {
  return validatedApproval(store, store.loadBatch(batchId), file, approvedPlan);
}

function samePreflightMeaning(a: Preflight, b: Preflight): boolean {
  const { inspected_at: _aTime, digest: _aDigest, ...left } = a;
  const { inspected_at: _bTime, digest: _bDigest, ...right } = b;
  return canonical(left) === canonical(right);
}

export function publicError(error: unknown): string {
  if (error instanceof ChannelError) return error.message;
  if (error instanceof Error) return error.message.slice(0, 256);
  return 'operation failed';
}

async function runTarget(store: Store, batch: Batch, target: FrozenTarget, adapter: ChannelAdapter,
  approval: Approval, resume: boolean): Promise<void> {
  const row = store.target(batch.batch_id, target.channel);
  if (row.phase === 'submitted') return;
  const existing = store.operations(batch.batch_id, target.channel);
  if (!resume && existing.length) throw new Error('target has an earlier operation; use resume after checking its status');
  if (!existing.length) {
    const observed = await adapter.preflight();
    const saved = JSON.parse(row.preflight) as Preflight;
    if (!samePreflightMeaning(observed, saved)) throw new Error('remote baseline changed after review; run preflight and review again');
  }
  for (const step of adapter.steps) {
    const previous = store.latestOperation(batch.batch_id, target.channel, step);
    if (previous?.effect === 'confirmed_applied') continue;
    if (previous && ['intent', 'unknown', 'confirmed_rejected'].includes(previous.effect)) throw new Error(`${step}: result is unresolved; run reconcile`);
    if (adapter.waitBeforeStep) await adapter.waitBeforeStep(step);
    const checkpoints = Object.fromEntries(store.operations(batch.batch_id, target.channel)
      .filter(op => op.effect === 'confirmed_applied').map(op => [op.step, op.remote_ref]));
    const requestDigest = digest({ batch: batch.digest, approval: approval.approval_plan,
      preflight: row.preflight_digest, channel: target.channel, step, checkpoints });
    const operation = store.beginOperation(batch.batch_id, target.channel, step, requestDigest);
    try {
      const receipt = await adapter.execute(step, operation.id);
      if (!receipt.ref || receipt.ref.length > 2048 || /[\r\n\u0000]/.test(receipt.ref)) throw new Error('vendor receipt is missing or invalid');
      store.finishOperation(operation, 'confirmed_applied', receipt.ref, receipt.material);
    } catch (error) {
      store.finishOperation(operation, error instanceof ChannelError && error.effect === 'not_sent' ? 'confirmed_not_applied' : 'unknown');
      throw error;
    }
  }
  const last = store.latestOperation(batch.batch_id, target.channel, adapter.steps.at(-1)!);
  if (!last || last.effect !== 'confirmed_applied') throw new Error('submit receipt missing');
  store.setSubmitted(batch.batch_id, target.channel, last.remote_ref);
}

export async function submit(store: Store, batchId: string, approvalFile: string, approvedPlan: string,
  credentials: CredentialSource, tools: ArtifactInspector, resume = false, fetcher: typeof fetch = fetch): Promise<Record<string, unknown>> {
  const batch = store.loadBatch(batchId);
  const approval = validatedApproval(store, batch, approvalFile, approvedPlan);
  // Verify every selected artifact before the first channel can write.
  for (const target of selectedTargets(batch, approval.selected_channels)) {
    if (store.target(batchId, target.channel).phase === 'submitted') continue;
    verifyFrozen(tools, target.artifact);
    if (target.icon_path && (hashFile(target.icon_path).sha256 !== target.icon_sha256)) throw new Error('frozen icon changed');
    const row = store.target(batchId, target.channel);
    store.assertScopeAvailable(row.scope, batchId, target.channel);
  }
  const errors: Record<string, string> = {};
  for (const target of selectedTargets(batch, approval.selected_channels)) {
    try {
      if (store.target(batchId, target.channel).phase === 'submitted') continue;
      const secrets = await credentials.forChannel(target.channel);
      const adapter = channelAdapter(batch, target, secrets, store, fetcher);
      await runTarget(store, batch, target, adapter, approval, resume);
    } catch (error) { errors[target.channel] = publicError(error); }
  }
  return { ...store.summary(batchId), effect: Object.keys(errors).length ? 'partial_or_unknown' : 'submitted', errors };
}

export async function refresh(store: Store, batchId: string, credentials: CredentialSource,
  channels?: Channel[], fetcher: typeof fetch = fetch): Promise<Record<string, unknown>> {
  const batch = store.loadBatch(batchId);
  const errors: Record<string, string> = {};
  for (const target of selectedTargets(batch, channels)) {
    try {
      const secrets = await credentials.forChannel(target.channel);
      const adapter = channelAdapter(batch, target, secrets, store, fetcher);
      const observed = await adapter.observe(store.target(batchId, target.channel).remote_ref);
      store.saveObservation(batchId, target.channel, observed);
    } catch (error) {
      errors[target.channel] = publicError(error);
      store.saveQueryFailure(batchId, target.channel, errors[target.channel]!);
    }
  }
  return { ...store.summary(batchId), effect: 'read_only', errors };
}

export async function reconcile(store: Store, batchId: string, credentials: CredentialSource,
  channels?: Channel[], fetcher: typeof fetch = fetch): Promise<Record<string, unknown>> {
  const batch = store.loadBatch(batchId);
  const errors: Record<string, string> = {};
  for (const target of selectedTargets(batch, channels)) {
    const tokenIssue = store.recovery(batchId, target.channel).find(issue => issue.kind === 'account_token');
    if (tokenIssue) { errors[target.channel] = tokenIssue.message; continue; }
    const pending = store.operations(batchId, target.channel).filter(op => ['intent', 'unknown'].includes(op.effect));
    if (!pending.length) {
      const conflict = store.recovery(batchId, target.channel)[0];
      if (conflict) errors[target.channel] = conflict.message;
      continue;
    }
    try {
      const secrets = await credentials.forChannel(target.channel);
      const adapter = channelAdapter(batch, target, secrets, store, fetcher);
      for (const op of pending) {
        const result = await adapter.reconcile(op);
        if (result.effect === 'unknown') {
          try { store.saveObservation(batchId, target.channel, await adapter.observe(store.target(batchId, target.channel).remote_ref)); }
          catch { /* No read-only source can prove this write. Keep the durable block. */ }
          errors[target.channel] = 'read-only evidence cannot resolve this write'; continue;
        }
        store.resolveOperation(op, result.effect, result.ref ?? '');
      }
    } catch (error) { errors[target.channel] = publicError(error); }
  }
  return { ...store.summary(batchId), effect: 'read_only', errors };
}

export function report(store: Store, batchId: string): string {
  const batch = store.loadBatch(batchId);
  const summary = store.summary(batchId);
  const lines = [`# ${batch.batch_id} 发布报告`, '', `包名：${batch.package_name}。`,
    `版本：${batch.version_name}（${batch.version_code}）。`, `批次摘要：\`${batch.digest}\`。`, '',
    '| 渠道 | 执行 | 审核 | 上架 | 查询 |', '| --- | --- | --- | --- | --- |'];
  const targets = summary.targets as Array<Record<string, string>>;
  for (const row of targets) lines.push(`| ${row.channel} | ${row.execution_phase} | ${row.review_state} | ${row.availability_state} | ${row.query_status} |`);
  for (const target of batch.targets) {
    for (const issue of store.recovery(batchId, target.channel)) {
      lines.push('', `${target.channel}：${issue.message}`, `记录：${issue.operation_id ?? '账号令牌'}；时间：${issue.recorded_at}。`,
        `下一步：${issue.next_action}`);
    }
  }
  lines.push('', '本报告只记录本地回执和已关联的观测。审核与上架状态以各字段为准。', '');
  return lines.join('\n');
}
