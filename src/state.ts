import fs from 'node:fs';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { type Approval, type Availability, type Batch, type Channel, digest, type Effect, type Phase, type Preflight, type Review } from './model.js';
import { dataRoot, privateDir } from './private-file.js';

export interface TargetRow {
  batch_id: string;
  channel: Channel;
  scope: string;
  phase: Phase;
  review: Review;
  availability: Availability;
  preflight: string;
  preflight_digest: string;
  remote_ref: string;
  query_status: string;
  last_success_at: string;
  last_error: string;
  disputed: number;
}
export interface OperationRow {
  id: string;
  batch_id: string;
  channel: Channel;
  scope: string;
  step: string;
  attempt: number;
  request_digest: string;
  effect: Effect;
  remote_ref: string;
  started_at: string;
  completed_at: string;
}
export interface Observation {
  review: Review;
  availability: Availability;
  association: 'confirmed' | 'unconfirmed';
  source: string;
  remote_ref?: string;
  observed_at: string;
  context?: Record<string, string>;
}

function rows<T>(db: DatabaseSync, sql: string, ...params: (string | number)[]): T[] {
  return db.prepare(sql).all(...params) as T[];
}
function row<T>(db: DatabaseSync, sql: string, ...params: (string | number)[]): T | undefined {
  return db.prepare(sql).get(...params) as T | undefined;
}

export class Store {
  readonly db: DatabaseSync;
  readonly root: string;

  constructor(root = dataRoot()) {
    process.umask(0o077);
    this.root = root;
    privateDir(root);
    const file = path.join(root, 'state.sqlite');
    if (fs.existsSync(file) && (!fs.lstatSync(file).isFile() || fs.lstatSync(file).isSymbolicLink())) throw new Error('state path is not a regular file');
    this.db = new DatabaseSync(file, { timeout: 1000 });
    fs.chmodSync(file, 0o600);
    this.db.exec('PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=1000;');
    const version = row<{ user_version: number }>(this.db, 'PRAGMA user_version')?.user_version ?? 0;
    if (version !== 0 && version !== 2) throw new Error(`unsupported state schema: ${version}`);
    if (version === 0) this.db.exec(`
      BEGIN IMMEDIATE;
      CREATE TABLE batches (
        id TEXT PRIMARY KEY, digest TEXT NOT NULL UNIQUE, document TEXT NOT NULL,
        created_at TEXT NOT NULL
      );
      CREATE TABLE targets (
        batch_id TEXT NOT NULL REFERENCES batches(id), channel TEXT NOT NULL,
        scope TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'prepared',
        review TEXT NOT NULL DEFAULT 'unknown', availability TEXT NOT NULL DEFAULT 'unknown',
        preflight TEXT NOT NULL DEFAULT '', preflight_digest TEXT NOT NULL DEFAULT '',
        remote_ref TEXT NOT NULL DEFAULT '', query_status TEXT NOT NULL DEFAULT 'never',
        last_success_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
        disputed INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(batch_id, channel)
      );
      CREATE TABLE operations (
        id TEXT PRIMARY KEY, batch_id TEXT NOT NULL, channel TEXT NOT NULL, scope TEXT NOT NULL,
        step TEXT NOT NULL, attempt INTEGER NOT NULL, request_digest TEXT NOT NULL,
        effect TEXT NOT NULL CHECK(effect IN ('intent','unknown','confirmed_applied','confirmed_rejected','confirmed_not_applied')),
        remote_ref TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '',
        UNIQUE(batch_id, channel, step, attempt),
        FOREIGN KEY(batch_id, channel) REFERENCES targets(batch_id, channel)
      );
      CREATE INDEX operations_scope ON operations(scope, effect);
      CREATE TABLE materials (
        operation_id TEXT PRIMARY KEY REFERENCES operations(id),
        document TEXT NOT NULL, expires_at TEXT NOT NULL
      );
      CREATE TABLE token_leases (
        scope TEXT PRIMARY KEY, effect TEXT NOT NULL, token TEXT NOT NULL DEFAULT '',
        expires_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
      );
      CREATE TABLE pacing (scope TEXT PRIMARY KEY, next_at INTEGER NOT NULL);
      CREATE TABLE observations (
        id INTEGER PRIMARY KEY, batch_id TEXT NOT NULL, channel TEXT NOT NULL,
        document TEXT NOT NULL, created_at TEXT NOT NULL,
        FOREIGN KEY(batch_id, channel) REFERENCES targets(batch_id, channel)
      );
      PRAGMA user_version=2;
      COMMIT;
    `);
  }

  close(): void { this.db.close(); }
  private tx<T>(use: () => T): T {
    this.db.exec('BEGIN IMMEDIATE');
    try { const result = use(); this.db.exec('COMMIT'); return result; }
    catch (e) { this.db.exec('ROLLBACK'); throw e; }
  }

  saveBatch(batch: Batch): void {
    const { digest: declaredDigest, created_at: _created, ...content } = batch;
    if (declaredDigest !== digest(content) || new Set(batch.targets.map(target => target.channel)).size !== batch.targets.length ||
      !batch.targets.length) throw new Error('batch content is not a valid immutable intent');
    const existing = this.loadBatchOrNull(batch.batch_id);
    if (existing) {
      if (existing.digest !== batch.digest) throw new Error('batch ID already has another immutable intent');
      return;
    }
    this.tx(() => {
      this.db.prepare('INSERT INTO batches(id,digest,document,created_at) VALUES (?,?,?,?)')
        .run(batch.batch_id, batch.digest, JSON.stringify(batch), batch.created_at);
      for (const target of batch.targets) {
        const scope = digest([target.channel, target.account, target.app_id, batch.package_name]);
        this.db.prepare('INSERT INTO targets(batch_id,channel,scope) VALUES (?,?,?)').run(batch.batch_id, target.channel, scope);
      }
    });
  }

  private loadBatchOrNull(id: string): Batch | null {
    const found = row<{ document: string; digest: string }>(this.db, 'SELECT document,digest FROM batches WHERE id=?', id);
    if (!found) return null;
    const batch = JSON.parse(found.document) as Batch;
    const { digest: declaredDigest, created_at: _created, ...content } = batch;
    if (batch.digest !== found.digest || declaredDigest !== digest(content)) throw new Error('saved batch digest differs');
    return batch;
  }
  loadBatch(id: string): Batch { return this.loadBatchOrNull(id) ?? (() => { throw new Error('batch not found'); })(); }
  batchIds(): string[] { return rows<{ id: string }>(this.db, 'SELECT id FROM batches ORDER BY created_at DESC').map(row => row.id); }
  targets(id: string): TargetRow[] { return rows<TargetRow>(this.db, 'SELECT * FROM targets WHERE batch_id=? ORDER BY channel', id); }
  target(id: string, channel: Channel): TargetRow {
    return row<TargetRow>(this.db, 'SELECT * FROM targets WHERE batch_id=? AND channel=?', id, channel) ?? (() => { throw new Error('target not found'); })();
  }

  savePreflight(id: string, result: Preflight): void {
    const batch = this.loadBatch(id);
    const frozen = batch.targets.find(target => target.channel === result.channel);
    const { digest: recorded, ...content } = result;
    if (!frozen || recorded !== digest(content) || result.account !== frozen.account ||
      result.app_id !== frozen.app_id || result.package_name !== batch.package_name ||
      result.version_code !== batch.version_code) throw new Error('preflight does not match the frozen target');
    this.tx(() => {
      const target = this.target(id, result.channel);
      if (target.phase === 'submitted' || target.phase === 'unknown' || this.operations(id, result.channel).length)
        throw new Error('cannot replace preflight after a write');
      this.assertScopeAvailable(target.scope, id, result.channel);
      this.db.prepare("UPDATE targets SET preflight=?,preflight_digest=?,phase='preflighted' WHERE batch_id=? AND channel=?")
        .run(JSON.stringify(result), result.digest, id, result.channel);
    });
  }

  assertScopeAvailable(scope: string, batchId: string, channel: Channel): void {
    const conflict = row<{ id: string }>(this.db,
      "SELECT id FROM operations WHERE scope=? AND effect IN ('intent','unknown') AND (batch_id<>? OR channel<>?) LIMIT 1",
      scope, batchId, channel);
    if (conflict) throw new Error('another unresolved write owns this channel, account and application');
  }

  operations(id: string, channel?: Channel): OperationRow[] {
    return channel
      ? rows<OperationRow>(this.db, 'SELECT * FROM operations WHERE batch_id=? AND channel=? ORDER BY started_at,attempt', id, channel)
      : rows<OperationRow>(this.db, 'SELECT * FROM operations WHERE batch_id=? ORDER BY started_at,attempt', id);
  }
  latestOperation(id: string, channel: Channel, step: string): OperationRow | undefined {
    return row<OperationRow>(this.db,
      'SELECT * FROM operations WHERE batch_id=? AND channel=? AND step=? ORDER BY attempt DESC LIMIT 1', id, channel, step);
  }
  operationById(id: string): OperationRow | undefined {
    return row<OperationRow>(this.db, 'SELECT * FROM operations WHERE id=?', id);
  }

  // The committed intent must exist before any business HTTP request is sent.
  beginOperation(id: string, channel: Channel, step: string, requestDigest: string): OperationRow {
    return this.tx(() => {
      const target = this.target(id, channel);
      if (!target.preflight_digest) throw new Error('preflight is required before a write');
      this.assertScopeAvailable(target.scope, id, channel);
      const prior = this.latestOperation(id, channel, step);
      if (prior && prior.effect !== 'confirmed_not_applied') throw new Error(`step ${step} is already attempted; reconcile before continuing`);
      const attempt = (prior?.attempt ?? 0) + 1;
      const idempotentId = digest([id, channel, step, attempt, requestDigest]);
      const started = new Date().toISOString();
      this.db.prepare('INSERT INTO operations(id,batch_id,channel,scope,step,attempt,request_digest,effect,started_at) VALUES (?,?,?,?,?,?,?,?,?)')
        .run(idempotentId, id, channel, target.scope, step, attempt, requestDigest, 'intent', started);
      return this.latestOperation(id, channel, step)!;
    });
  }

  finishOperation(op: OperationRow, effect: Effect, remoteRef = '', material?: { value: unknown; expiresAt: string }): void {
    if (effect === 'intent') throw new Error('invalid completed effect');
    this.tx(() => {
      const current = row<OperationRow>(this.db, 'SELECT * FROM operations WHERE id=?', op.id);
      if (!current || current.effect !== 'intent') throw new Error('operation intent changed before completion');
      this.db.prepare('UPDATE operations SET effect=?,remote_ref=?,completed_at=? WHERE id=?')
        .run(effect, remoteRef, new Date().toISOString(), op.id);
      if (material && effect === 'confirmed_applied') this.db.prepare('INSERT INTO materials(operation_id,document,expires_at) VALUES (?,?,?)')
        .run(op.id, JSON.stringify(material.value), material.expiresAt);
      if (effect === 'unknown') this.db.prepare("UPDATE targets SET phase='unknown' WHERE batch_id=? AND channel=?").run(op.batch_id, op.channel);
      if (effect === 'confirmed_applied') this.db.prepare('UPDATE targets SET remote_ref=? WHERE batch_id=? AND channel=?').run(remoteRef, op.batch_id, op.channel);
    });
  }

  readMaterial(op: OperationRow): unknown {
    const found = row<{ document: string; expires_at: string }>(this.db, 'SELECT document,expires_at FROM materials WHERE operation_id=?', op.id);
    if (!found || Date.parse(found.expires_at) <= Date.now()) throw new Error('saved upload authorization is missing or expired; manual recovery is required');
    return JSON.parse(found.document) as unknown;
  }

  beginToken(scope: string): string | null {
    return this.tx(() => {
      const existing = row<{ effect: string; token: string; expires_at: string }>(this.db,
        'SELECT effect,token,expires_at FROM token_leases WHERE scope=?', scope);
      if (existing?.effect === 'confirmed_applied' && Date.parse(existing.expires_at) > Date.now() + 60_000) return existing.token;
      if (existing && ['intent', 'unknown'].includes(existing.effect)) throw new Error('account token result is unresolved; inspect the account before another token request');
      this.db.prepare("INSERT INTO token_leases(scope,effect,token,expires_at,updated_at) VALUES (?,'intent','','',?) ON CONFLICT(scope) DO UPDATE SET effect='intent',token='',expires_at='',updated_at=excluded.updated_at")
        .run(scope, new Date().toISOString());
      return null;
    });
  }
  finishToken(scope: string, token: string, expiresAt: string): void {
    this.tx(() => {
      const current = row<{ effect: string }>(this.db, 'SELECT effect FROM token_leases WHERE scope=?', scope);
      if (current?.effect !== 'intent') throw new Error('token intent changed');
      this.db.prepare("UPDATE token_leases SET effect='confirmed_applied',token=?,expires_at=?,updated_at=? WHERE scope=?")
        .run(token, expiresAt, new Date().toISOString(), scope);
    });
  }
  unknownToken(scope: string): void {
    this.tx(() => this.db.prepare("UPDATE token_leases SET effect='unknown',token='',updated_at=? WHERE scope=? AND effect='intent'")
      .run(new Date().toISOString(), scope));
  }

  reservePacing(scope: string, intervalMs: number): number {
    return this.tx(() => {
      const prior = row<{ next_at: number }>(this.db, 'SELECT next_at FROM pacing WHERE scope=?', scope);
      const now = Date.now();
      const start = Math.max(now, prior?.next_at ?? now);
      this.db.prepare('INSERT INTO pacing(scope,next_at) VALUES (?,?) ON CONFLICT(scope) DO UPDATE SET next_at=excluded.next_at')
        .run(scope, start + intervalMs);
      return start - now;
    });
  }

  resolveOperation(op: OperationRow, effect: 'confirmed_applied' | 'confirmed_not_applied', remoteRef = ''): void {
    this.tx(() => {
      const current = row<OperationRow>(this.db, 'SELECT * FROM operations WHERE id=?', op.id);
      if (!current || !['intent', 'unknown'].includes(current.effect)) throw new Error('operation is not unresolved');
      this.db.prepare('UPDATE operations SET effect=?,remote_ref=?,completed_at=? WHERE id=?')
        .run(effect, remoteRef, new Date().toISOString(), op.id);
      this.db.prepare("UPDATE targets SET phase='preflighted',remote_ref=? WHERE batch_id=? AND channel=?")
        .run(remoteRef, op.batch_id, op.channel);
    });
  }

  setSubmitted(id: string, channel: Channel, ref: string): void {
    this.tx(() => this.db.prepare("UPDATE targets SET phase='submitted',remote_ref=? WHERE batch_id=? AND channel=?")
      .run(ref, id, channel));
  }

  saveObservation(id: string, channel: Channel, observed: Observation): void {
    this.tx(() => {
      const target = this.target(id, channel);
      this.db.prepare('INSERT INTO observations(batch_id,channel,document,created_at) VALUES (?,?,?,?)')
        .run(id, channel, JSON.stringify(observed), observed.observed_at);
      const confirmed = observed.association === 'confirmed' && target.phase === 'submitted';
      const review = confirmed ? observed.review : target.review;
      const availability = confirmed ? observed.availability : target.availability;
      this.db.prepare('UPDATE targets SET review=?,availability=?,query_status=?,last_success_at=?,last_error=? WHERE batch_id=? AND channel=?')
        .run(review, availability, 'ok', observed.observed_at, '', id, channel);
    });
  }
  saveQueryFailure(id: string, channel: Channel, reason: string): void {
    this.tx(() => this.db.prepare('UPDATE targets SET query_status=?,last_error=? WHERE batch_id=? AND channel=?')
      .run('failed', reason.slice(0, 256), id, channel));
  }
  observations(id: string, channel?: Channel): Observation[] {
    const found = channel
      ? rows<{ document: string }>(this.db, 'SELECT document FROM observations WHERE batch_id=? AND channel=? ORDER BY id', id, channel)
      : rows<{ document: string }>(this.db, 'SELECT document FROM observations WHERE batch_id=? ORDER BY id', id);
    return found.map(r => JSON.parse(r.document) as Observation);
  }

  summary(id: string): Record<string, unknown> {
    const batch = this.loadBatch(id);
    const targets = this.targets(id).map(target => {
      const last = this.observations(id, target.channel).at(-1);
      const context = last && (last.association === 'unconfirmed' || target.phase !== 'submitted')
        ? { association: 'unconfirmed', source: last.source, observed_at: last.observed_at, ...last.context }
        : undefined;
      return { channel: target.channel, execution_phase: target.phase, review_state: target.review,
        availability_state: target.availability, query_status: target.query_status,
        last_success_at: target.last_success_at, last_error: target.last_error,
        recovery_required: this.operations(id, target.channel).some(op => ['intent', 'unknown'].includes(op.effect)),
        ...(context ? { channel_context: context } : {}) };
    });
    const accepted = targets.filter(t => t.execution_phase === 'submitted').length;
    const approved = targets.filter(t => t.review_state === 'approved').length;
    const live = targets.filter(t => t.availability_state === 'live').length;
    return { schema_version: 2, batch_id: id, batch_digest: batch.digest, total_targets: targets.length,
      accepted_targets: accepted, approved_targets: approved, live_targets: live,
      all_accepted: accepted === targets.length, all_approved: approved === targets.length,
      all_live: live === targets.length, targets };
  }

  verifyApproval(approval: Approval): void {
    const batch = this.loadBatch(approval.batch_id);
    if (batch.digest !== approval.batch_digest) throw new Error('approval refers to another immutable batch');
    for (const channel of approval.selected_channels) {
      const target = this.target(batch.batch_id, channel);
      if (!target.preflight_digest || target.preflight_digest !== approval.preflight_digests[channel]) throw new Error(`${channel}: preflight changed since review`);
    }
  }
}
