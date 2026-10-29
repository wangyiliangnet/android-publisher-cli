import { createHash } from 'node:crypto';
import path from 'node:path';

export const CHANNELS = ['huawei', 'honor', 'xiaomi', 'oppo', 'vivo', 'tencent'] as const;
export type Channel = typeof CHANNELS[number];
export type Effect = 'intent' | 'unknown' | 'confirmed_applied' | 'confirmed_rejected' | 'confirmed_not_applied';
export type Phase = 'prepared' | 'preflighted' | 'submitted' | 'unknown';
export type Review = 'unknown' | 'in_review' | 'approved' | 'rejected';
export type Availability = 'unknown' | 'live' | 'not_live';

export interface ProjectChannel {
  channel: Channel;
  account: string;
  app_id: string;
  client_id?: string;
  language_id?: string;
  icon_path?: string;
  privacy_url?: string;
}
export interface Project {
  schema_version: 2;
  project_id: string;
  display_name?: string;
  package_name?: string;
  channels: ProjectChannel[];
}
export interface BatchInput {
  schema_version: 2;
  batch_id: string;
  project_id: string;
  release_notes_file: string;
  apks: Partial<Record<Channel, string>>;
}
export interface Artifact {
  path: string;
  filename: string;
  sha256: string;
  size: number;
  signer_sha256: string;
  package_name: string;
  version_name: string;
  version_code: string;
  abis: string[];
}
export interface FrozenTarget {
  channel: Channel;
  account: string;
  app_id: string;
  client_id?: string;
  language_id?: string;
  icon_path?: string;
  icon_sha256?: string;
  icon_size?: number;
  privacy_url?: string;
  artifact: Artifact;
}
export interface Batch {
  schema_version: 2;
  batch_id: string;
  project_id: string;
  package_name: string;
  version_name: string;
  version_code: string;
  signer_sha256: string;
  release_notes: string;
  release_policy: 'after_approval';
  targets: FrozenTarget[];
  digest: string;
  created_at: string;
}
export interface Preflight {
  channel: Channel;
  account: string;
  app_id: string;
  package_name: string;
  version_code: string;
  remote_version_code?: string;
  remote_status?: string;
  remote_ref?: string;
  metadata?: Record<string, string>;
  inspected_at: string;
  digest: string;
}
export interface Approval {
  schema_version: 2;
  batch_id: string;
  batch_digest: string;
  selected_channels: Channel[];
  target_accounts: Array<{ channel: Channel; account: string; app_id: string; client_id?: string }>;
  preflight_digests: Record<string, string>;
  package_name: string;
  version_name: string;
  version_code: string;
  artifact_sha256: Record<string, string>;
  signer_sha256: string;
  release_notes: string;
  release_policy: 'after_approval';
  approval_plan: string;
}

export const validId = (s: unknown): s is string => typeof s === 'string' && /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(s);
export const validHash = (s: unknown): s is string => typeof s === 'string' && /^[a-f0-9]{64}$/.test(s);
export const validPackage = (s: unknown): s is string => typeof s === 'string' && /^[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$/.test(s) && s.length <= 255;
export const validVersion = (s: unknown): s is string => typeof s === 'string' && /^[1-9][0-9]{0,18}$/.test(s) && BigInt(s) <= 9223372036854775807n;
export const validText = (s: unknown, max: number): s is string => typeof s === 'string' && Buffer.byteLength(s) <= max && !/[\u0000-\u001f\u007f\u2028\u2029]/u.test(s);
export const validAbsolute = (s: unknown): s is string => typeof s === 'string' && s.length <= 4096 && path.isAbsolute(s) && path.normalize(s) === s && validText(s, 4096);
export const isChannel = (s: unknown): s is Channel => typeof s === 'string' && CHANNELS.includes(s as Channel);

export function assertObject(value: unknown, keys: readonly string[], optional: readonly string[] = []): asserts value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('expected an object');
  const got = Object.keys(value);
  if (got.some(k => !keys.includes(k) && !optional.includes(k)) || keys.some(k => !got.includes(k))) throw new Error('unexpected or missing field');
}

// JSON.parse silently accepts duplicate identity keys. Check the token stream first.
export function parseStrictJSON(source: string): unknown {
  if (Buffer.byteLength(source) > 8 * 1024 * 1024) throw new Error('JSON input is too large');
  let i = 0;
  const ws = () => { while (i < source.length && /[ \t\r\n]/.test(source[i]!)) i++; };
  const quoted = (): string => {
    const start = i++;
    while (i < source.length) {
      const ch = source[i++];
      if (ch === '\\') { i++; continue; }
      if (ch === '"') return JSON.parse(source.slice(start, i)) as string;
    }
    throw new Error('unterminated JSON string');
  };
  const scan = (depth: number): unknown => {
    if (depth > 32) throw new Error('JSON nesting is too deep');
    ws();
    if (source[i] === '{') {
      i++; ws(); const seen = new Set<string>(); const result: Record<string, unknown> = {};
      if (source[i] === '}') { i++; return result; }
      for (;;) {
        ws(); if (source[i] !== '"') throw new Error('invalid JSON object');
        const key = quoted(); if (seen.has(key) || key === '__proto__' || key === 'constructor') throw new Error(`duplicate or unsafe JSON field: ${key}`);
        seen.add(key); ws(); if (source[i++] !== ':') throw new Error('invalid JSON object');
        result[key] = scan(depth + 1); ws(); const end = source[i++];
        if (end === '}') return result;
        if (end !== ',') throw new Error('invalid JSON object');
      }
    }
    if (source[i] === '[') {
      i++; ws(); const result: unknown[] = []; if (source[i] === ']') { i++; return result; }
      for (;;) { result.push(scan(depth + 1)); ws(); const end = source[i++]; if (end === ']') return result; if (end !== ',') throw new Error('invalid JSON array'); }
    }
    if (source[i] === '"') return quoted();
    const start = i;
    while (i < source.length && !/[ \t\r\n,}\]]/.test(source[i]!)) i++;
    if (i === start) throw new Error('invalid JSON value');
    const token = source.slice(start, i);
    const value = JSON.parse(token) as unknown;
    if (typeof value === 'number' && /^-?(?:0|[1-9][0-9]*)$/.test(token) && !Number.isSafeInteger(value)) return BigInt(token);
    return value;
  };
  const value = scan(0); ws(); if (i !== source.length) throw new Error('trailing JSON content');
  return value;
}

export function canonical(value: unknown): string {
  if (typeof value === 'bigint') return value.toString();
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    const fields = Object.entries(value).filter(([, v]) => v !== undefined).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0);
    return `{${fields.map(([k, v]) => `${JSON.stringify(k)}:${canonical(v)}`).join(',')}}`;
  }
  return JSON.stringify(value);
}
export const digest = (value: unknown): string => createHash('sha256').update(typeof value === 'string' ? value : canonical(value)).digest('hex');

export function validateProject(value: unknown): Project {
  assertObject(value, ['schema_version', 'project_id', 'channels'], ['display_name', 'package_name', 'signer_sha256']);
  if (value.schema_version !== 2 || !validId(value.project_id) || !Array.isArray(value.channels) || value.channels.length < 1 || value.channels.length > CHANNELS.length) throw new Error('invalid project identity');
  if (value.package_name !== undefined && !validPackage(value.package_name)) throw new Error('invalid saved project identity');
  if (value.display_name !== undefined && (!validText(value.display_name, 256) || !value.display_name.trim())) throw new Error('invalid application display name');
  if (value.signer_sha256 !== undefined && !validHash(value.signer_sha256)) throw new Error('invalid saved project identity');
  const seen = new Set<string>();
  for (const row of value.channels) {
    assertObject(row, ['channel', 'account', 'app_id'], ['client_id', 'language_id', 'icon_path', 'privacy_url']);
    if (!isChannel(row.channel) || seen.has(row.channel) || !validId(row.account) || !validId(row.app_id)) throw new Error('invalid or duplicate channel');
    seen.add(row.channel);
    if (row.client_id !== undefined && !validId(row.client_id)) throw new Error('invalid client_id');
    if (row.language_id !== undefined && (!validText(row.language_id, 32) || !/^[A-Za-z][A-Za-z0-9_-]*$/.test(row.language_id))) throw new Error('invalid language_id');
    if (row.icon_path !== undefined && !validAbsolute(row.icon_path)) throw new Error('invalid icon_path');
    if (row.privacy_url !== undefined) {
      if (!validText(row.privacy_url, 4096)) throw new Error('invalid privacy_url');
      const url = new URL(row.privacy_url);
      if (url.protocol !== 'https:' || url.username || url.password || url.hash || url.search || !url.hostname) throw new Error('invalid privacy_url');
    }
    if (row.channel === 'xiaomi' && (!row.icon_path || !row.privacy_url || !row.client_id)) throw new Error('Xiaomi needs client_id, icon_path and privacy_url');
    if (['honor', 'vivo', 'tencent'].includes(row.channel) && !row.client_id) throw new Error(`${row.channel} needs client_id`);
  }
  return value as unknown as Project;
}

export function validateBatchInput(value: unknown): BatchInput {
  assertObject(value, ['schema_version', 'batch_id', 'project_id', 'release_notes_file', 'apks']);
  if (value.schema_version !== 2 || !validId(value.batch_id) || !validId(value.project_id) || !validAbsolute(value.release_notes_file)) throw new Error('invalid batch input');
  if (!value.apks || typeof value.apks !== 'object' || Array.isArray(value.apks) || !Object.keys(value.apks).length) throw new Error('missing APK paths');
  for (const [channel, apk] of Object.entries(value.apks)) if (!isChannel(channel) || !validAbsolute(apk)) throw new Error('invalid APK channel or path');
  return value as unknown as BatchInput;
}
