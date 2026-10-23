import { createHash } from 'node:crypto';
import path from 'node:path';

export const channels = ['huawei', 'xiaomi', 'oppo', 'vivo', 'honor', 'tencent'] as const;
export const reviewLabels: Record<string, string> = { '待审核': 'pending', '审核中': 'in_review', '审核通过': 'approved', '审核未通过': 'rejected', '已撤回': 'withdrawn' };
export const availabilityLabels: Record<string, string> = { '待上架': 'not_observed_live', '定时发布': 'scheduled', '已上线': 'live', '已被新版本替代': 'superseded' };
export type Status = 'observed' | 'auth_required' | 'profile_busy' | 'evidence_insufficient' | 'blocked_navigation' | 'timed_out' | 'canceled' | 'invalid_input' | 'browser_unavailable';
export class Failure extends Error { constructor(public readonly status: Status) { super(status); } }
export interface Target {
  channel: string; account: string; app_id: string; scope: string; package_name: string;
  version_code: string; remote_ref: string; adapter_binding: string; intent_digest: string;
}
export interface Request {
  schema_version: 1; mode: 'simulation'; target: Target; url: string;
  profile_dir: string; evidence_dir: string; timeout_ms: number;
}
export const digest = (value: string | Buffer): string => createHash('sha256').update(value).digest('hex');
export const hash = (value: unknown): value is string => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
function object(value: unknown, keys: string[]): asserts value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).sort().join(',') !== keys.sort().join(',')) throw new Failure('invalid_input');
}
const text = (value: unknown, max: number): value is string => typeof value === 'string' && value.length > 0 && Buffer.byteLength(value) <= max && !/[\u0000-\u001f\u007f]/u.test(value);

// Reject duplicate keys before JSON.parse can silently replace an identity.
export function parseJSON(input: string): unknown {
  let i = 0;
  function white() { while (/\s/.test(input[i] ?? '') && i < input.length) i++; }
  function string(): string {
    const start = i++;
    while (i < input.length) {
      const ch = input[i++];
      if (ch === '\\') { i++; continue; }
      if (ch === '"') return JSON.parse(input.slice(start, i)) as string;
    }
    throw new Failure('invalid_input');
  }
  function value(depth: number): void {
    if (depth > 12) throw new Failure('invalid_input');
    white();
    if (input[i] === '{') {
      i++; white(); const keys = new Set<string>();
      if (input[i] === '}') { i++; return; }
      for (;;) {
        white(); if (input[i] !== '"') throw new Failure('invalid_input');
        const key = string(); if (keys.has(key)) throw new Failure('invalid_input'); keys.add(key);
        white(); if (input[i++] !== ':') throw new Failure('invalid_input'); value(depth + 1); white();
        const end = input[i++]; if (end === '}') return; if (end !== ',') throw new Failure('invalid_input');
      }
    }
    if (input[i] === '[') {
      i++; white(); if (input[i] === ']') { i++; return; }
      for (;;) { value(depth + 1); white(); const end = input[i++]; if (end === ']') return; if (end !== ',') throw new Failure('invalid_input'); }
    }
    if (input[i] === '"') { string(); return; }
    const start = i; while (i < input.length && !/[\s,}\]]/.test(input[i]!)) i++;
    if (start === i) throw new Failure('invalid_input'); JSON.parse(input.slice(start, i));
  }
  try { value(0); white(); if (i !== input.length) throw new Error(); return JSON.parse(input); }
  catch { throw new Failure('invalid_input'); }
}

export function parseRequest(input: string): Request {
  if (Buffer.byteLength(input) > 64 * 1024) throw new Failure('invalid_input');
  const value = parseJSON(input);
  object(value, ['schema_version', 'mode', 'target', 'url', 'profile_dir', 'evidence_dir', 'timeout_ms']);
  if (value.schema_version !== 1 || value.mode !== 'simulation') throw new Failure('invalid_input');
  const t = value.target;
  object(t, ['channel', 'account', 'app_id', 'scope', 'package_name', 'version_code', 'remote_ref', 'adapter_binding', 'intent_digest']);
  if (!(channels as readonly unknown[]).includes(t.channel) || !text(t.account, 256) || !text(t.app_id, 256) || !text(t.package_name, 256) || !/^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$/.test(t.package_name) || !text(t.remote_ref, 2048) || !hash(t.scope) || !hash(t.intent_digest) || !(t.adapter_binding === '' || hash(t.adapter_binding)) || typeof t.version_code !== 'string' || !/^[1-9][0-9]{0,18}$/.test(t.version_code) || BigInt(t.version_code) > 9223372036854775807n) throw new Failure('invalid_input');
  if (t.scope !== digest(JSON.stringify([t.channel, t.account, t.app_id, t.package_name]))) throw new Failure('invalid_input');
  if (typeof value.timeout_ms !== 'number' || !Number.isInteger(value.timeout_ms) || value.timeout_ms < 1 || value.timeout_ms > 30000) throw new Failure('invalid_input');
  if (typeof value.url !== 'string') throw new Failure('invalid_input');
  let url: URL; try { url = new URL(value.url); } catch { throw new Failure('invalid_input'); }
  if (url.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(url.hostname) || !url.port || url.username || url.password || url.hash || url.pathname !== `/browser/${t.channel}` || url.search !== `?scope=${t.scope}` || value.url !== url.href) throw new Failure('invalid_input');
  if (typeof value.profile_dir !== 'string' || typeof value.evidence_dir !== 'string' || !path.isAbsolute(value.profile_dir) || !path.isAbsolute(value.evidence_dir) || path.normalize(value.profile_dir) !== value.profile_dir || path.normalize(value.evidence_dir) !== value.evidence_dir) throw new Failure('invalid_input');
  const root = path.dirname(path.dirname(value.evidence_dir));
  if (path.basename(path.dirname(value.evidence_dir)) !== 'evidence' || !hash(path.basename(value.evidence_dir)) || value.profile_dir !== path.join(root, 'browser', t.channel as string, digest(t.account), 'profile') || root === '/') throw new Failure('invalid_input');
  return value as unknown as Request;
}
