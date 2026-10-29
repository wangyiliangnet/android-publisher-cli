import { randomUUID } from 'node:crypto';
import { canonical, digest, validId } from '../model.js';

export interface WebJob {
  id: string;
  request_id: string;
  action: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed';
  created_at: string;
  app_id?: string;
  release_id?: string;
  result?: unknown;
  error?: string;
}

const allowed = new Set(['apps.check', 'releases.prepare', 'releases.preflight', 'releases.review',
  'releases.submit', 'releases.get', 'releases.reconcile', 'doctor.repair']);

/** Browser-independent operations; durable business recovery belongs to the engine. */
export class WebJobs {
  private readonly jobs = new Map<string, { job: WebJob; fingerprint: string }>();
  constructor(private readonly enqueue: (work: () => Promise<void>) => Promise<void>,
    private readonly invoke: (action: string, payload: unknown) => Promise<unknown>,
    private readonly safeError: (action: string, error: unknown) => string) {}
  list(): WebJob[] { return [...this.jobs.values()].map(value => ({ ...value.job })); }
  get(id: unknown): WebJob {
    const record = typeof id === 'string' ? this.jobs.get(id) : undefined;
    if (!record) throw new Error('task not found');
    return { ...record.job };
  }
  start(value: unknown): WebJob {
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('invalid task');
    const input = value as Record<string, unknown>;
    if (Object.keys(input).some(key => !['action', 'payload', 'request_id'].includes(key)) ||
      typeof input.action !== 'string' || !allowed.has(input.action) || !validId(input.request_id) ||
      !input.payload || typeof input.payload !== 'object' || Array.isArray(input.payload)) throw new Error('invalid task');
    const action = input.action;
    const payload = input.payload as Record<string, unknown>;
    // No credential-bearing action can enter a task or a task result.
    if (Object.keys(payload).some(key => ['credentials', 'files', 'client_secret', 'api_secret', 'access_secret'].includes(key))) throw new Error('credential input is not a task');
    const fingerprint = digest(canonical({ action, payload }));
    const existing = [...this.jobs.values()].find(record => record.job.request_id === input.request_id);
    if (existing) {
      if (existing.fingerprint !== fingerprint) throw new Error('task identity already used');
      return { ...existing.job };
    }
    if (this.list().filter(job => ['queued', 'running'].includes(job.status)).length >= 20) throw new Error('too many pending tasks');
    for (const [id, record] of this.jobs) {
      if (this.jobs.size < 200) break;
      if (['succeeded', 'failed'].includes(record.job.status)) this.jobs.delete(id);
    }
    const job: WebJob = { id: randomUUID(), request_id: input.request_id, action, status: 'queued', created_at: new Date().toISOString(),
      ...(validId(payload.app_id) ? { app_id: payload.app_id } : {}),
      ...(validId(payload.release_id) ? { release_id: payload.release_id } : {}) };
    this.jobs.set(job.id, { job, fingerprint });
    if (job.release_id && !job.app_id) {
      void this.invoke('releases.get', { release_id: job.release_id, refresh: false }).then(value => {
        const appId = (value as { release?: { project_id?: unknown } } | null)?.release?.project_id;
        if (validId(appId)) job.app_id = appId;
      }).catch(() => {});
    }
    void this.enqueue(async () => {
      job.status = 'running';
      try {
        job.result = await this.invoke(action, payload);
        const result = job.result as { release?: { batch_id?: string; project_id?: string } } | null;
        if (result?.release?.batch_id) job.release_id = result.release.batch_id;
        if (result?.release?.project_id) job.app_id = result.release.project_id;
        job.status = 'succeeded';
      } catch (error) { job.error = this.safeError(action, error); job.status = 'failed'; }
    });
    return { ...job };
  }
}
