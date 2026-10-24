import { type Batch, type Channel, digest, type FrozenTarget, type Preflight } from '../model.js';
import { type CredentialSet } from '../credentials.js';
import { type Observation, type OperationRow, type Store } from '../state.js';
import { ChannelHTTP } from './http.js';

export interface StepReceipt {
  ref: string;
  material?: { value: unknown; expiresAt: string };
}
export interface ReconcileResult {
  effect: 'confirmed_applied' | 'confirmed_not_applied' | 'unknown';
  ref?: string;
}
export interface ChannelAdapter {
  readonly channel: Channel;
  readonly steps: readonly string[];
  preflight(): Promise<Preflight>;
  waitBeforeStep?(step: string): Promise<void>;
  execute(step: string, operationId: string): Promise<StepReceipt>;
  observe(remoteRef: string): Promise<Observation>;
  reconcile(operation: OperationRow): Promise<ReconcileResult>;
}

export abstract class BaseAdapter implements ChannelAdapter {
  abstract readonly channel: Channel;
  abstract readonly steps: readonly string[];
  protected readonly http: ChannelHTTP;
  constructor(readonly batch: Batch, readonly target: FrozenTarget, readonly secrets: CredentialSet,
    readonly store: Store, readonly fetcher: typeof fetch = fetch) {
    this.http = new ChannelHTTP(target.channel, fetcher, operationId => {
      const op = store.operationById(operationId);
      return op?.effect === 'intent' && op.channel === target.channel && op.batch_id === batch.batch_id;
    });
  }
  protected savedPreflight(): Preflight {
    const row = this.store.target(this.batch.batch_id, this.target.channel);
    if (!row.preflight || !row.preflight_digest) throw new Error('missing saved preflight');
    const value = JSON.parse(row.preflight) as Preflight;
    if (value.digest !== row.preflight_digest) throw new Error('preflight digest changed');
    return value;
  }
  protected material(step: string): unknown {
    const op = this.store.latestOperation(this.batch.batch_id, this.target.channel, step);
    if (!op || op.effect !== 'confirmed_applied') throw new Error(`missing confirmed ${step} receipt`);
    return this.store.readMaterial(op);
  }
  protected ref(step: string): string {
    const op = this.store.latestOperation(this.batch.batch_id, this.target.channel, step);
    if (!op || op.effect !== 'confirmed_applied' || !op.remote_ref) throw new Error(`missing confirmed ${step} receipt`);
    return op.remote_ref;
  }
  protected preflightRecord(remote: Omit<Preflight, 'channel' | 'account' | 'app_id' | 'package_name' | 'version_code' | 'inspected_at' | 'digest'>): Preflight {
    const value = { channel: this.target.channel, account: this.target.account, app_id: this.target.app_id,
      package_name: this.batch.package_name, version_code: this.batch.version_code,
      ...remote, inspected_at: new Date().toISOString() };
    return { ...value, digest: digest(value) };
  }
  protected observation(review: Observation['review'], availability: Observation['availability'],
    association: Observation['association'], source: string, remoteRef?: string, context?: Record<string, string>): Observation {
    return { review, availability, association, source, observed_at: new Date().toISOString(),
      ...(remoteRef ? { remote_ref: remoteRef } : {}), ...(context ? { context } : {}) };
  }
  abstract preflight(): Promise<Preflight>;
  abstract execute(step: string, operationId: string): Promise<StepReceipt>;
  abstract observe(remoteRef: string): Promise<Observation>;
  async reconcile(_operation: OperationRow): Promise<ReconcileResult> { return { effect: 'unknown' }; }
}
