import { createHmac, randomBytes } from 'node:crypto';
import fs from 'node:fs';
import { Readable } from 'node:stream';
import { type Channel, parseStrictJSON } from '../model.js';
import { hashFile } from '../private-file.js';

export const ORIGINS: Record<Channel, string> = {
  huawei: 'https://connect-api.cloud.huawei.com',
  honor: 'https://appmarket-openapi-drcn.cloud.honor.com',
  xiaomi: 'https://api.developer.xiaomi.com',
  oppo: 'https://oop-openapi-cn.heytapmobi.com',
  vivo: 'https://developer-api.vivo.com.cn',
  tencent: 'https://p.open.qq.com',
};

export type RequestClass = 'read' | 'lifecycle' | 'write';
export class ChannelError extends Error {
  constructor(readonly code: string, readonly effect: 'not_sent' | 'unknown' | 'confirmed_rejected' = 'unknown') {
    super(`channel protocol: ${code} (${effect})` +
      (code === 'token_rejected' ? '；请核对 API 账号标识和密钥，修正后重试原命令' : ''));
  }
}
export const unknown = (): ChannelError => new ChannelError('missing_or_unrecognized_evidence');
export function tokenRejected(channel: Channel, response: Record<string, unknown>): boolean {
  if ('access_token' in response || response.data && typeof response.data === 'object' && 'access_token' in response.data) return false;
  if (channel === 'honor') return typeof response.error === 'string' &&
    ['invalid_request', 'invalid_client', 'invalid_grant', 'unauthorized_client', 'unsupported_grant_type', 'invalid_scope'].includes(response.error);
  if (channel === 'oppo') return typeof response.errno === 'number' && Number.isSafeInteger(response.errno) && response.errno !== 0;
  return false;
}
export const object = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw unknown();
  return value as Record<string, unknown>;
};
export const list = (value: unknown): Record<string, unknown>[] => Array.isArray(value) ? value.map(object) : [];
export const str = (value: unknown): string => typeof value === 'string' ? value : '';
export const id = (value: unknown): string => {
  if (typeof value === 'string') return value;
  if (typeof value === 'bigint' && value >= 0n) return value.toString();
  if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) return String(value);
  return '';
};
export const num = (value: unknown): number | null => typeof value === 'number' && Number.isSafeInteger(value) ? value : null;
export const decimal = (value: unknown): string => typeof value === 'string' && /^[0-9]+$/.test(value) ? value :
  typeof value === 'bigint' && value >= 0n ? value.toString() :
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? String(value) : '';
export const required = (...values: string[]): void => { if (values.some(value => !value)) throw new ChannelError('missing_required_input', 'not_sent'); };
export function check(obj: Record<string, unknown>, key: string): void {
  const value = num(obj[key]);
  if (value === null) throw unknown();
  if (value !== 0) throw new ChannelError('vendor_business_error');
}
export function hmacSign(fields: Record<string, string>, secret: string, omit: string[] = []): string {
  const signed = Object.keys(fields).filter(k => !omit.includes(k)).sort().map(k => `${k}=${fields[k]}`).join('&');
  return createHmac('sha256', secret).update(signed).digest('hex');
}

function allowedUpload(channel: Channel, endpoint: URL): boolean {
  const host = endpoint.hostname.toLowerCase();
  if (channel === 'tencent') return host.endsWith('.myqcloud.com') && host.includes('.cos.');
  if (channel === 'huawei') return host === 'connect-api.cloud.huawei.com' || host === 'nsp-appgallery-agcfs-drcn.obs.cn-north-2.myhuaweicloud.cn' || host.endsWith('.dbankcdn.com');
  if (channel === 'oppo') return host === 'api.open.oppomobile.com' || host === 'oop-openapi-cn.heytapmobi.com' || host.endsWith('.heytapmobi.com');
  if (channel === 'honor') return host === 'appmarket-openapi-drcn.cloud.honor.com';
  return false;
}

export interface RequestOptions {
  method: 'GET' | 'POST' | 'PUT';
  path?: string;
  url?: string;
  class: RequestClass;
  operationId?: string;
  headers?: Record<string, string>;
  body?: Uint8Array | Readable;
  size?: number;
  upload?: boolean;
  rawUpload?: boolean;
}
export interface UploadFile { path: string; filename: string; sha256: string; size: number; }

export class ChannelHTTP {
  constructor(readonly channel: Channel, private readonly fetcher: typeof fetch = fetch,
    private readonly writeGuard: (operationId: string) => boolean = () => false) {}

  async send(q: RequestOptions): Promise<Record<string, unknown>> {
    if (q.class === 'write' && (!q.operationId || !this.writeGuard(q.operationId))) throw new ChannelError('unlogged_write', 'not_sent');
    if (q.class !== 'write' && q.operationId) throw new ChannelError('unexpected_operation', 'not_sent');
    if (q.path === undefined && q.url === undefined || q.path !== undefined && q.url !== undefined) throw new ChannelError('invalid_endpoint', 'not_sent');
    const endpoint = new URL(q.path === undefined ? q.url! : `${ORIGINS[this.channel]}${q.path}`);
    if (endpoint.protocol !== 'https:' || endpoint.username || endpoint.password || endpoint.hash || endpoint.port ||
      (q.upload ? !allowedUpload(this.channel, endpoint) : endpoint.origin !== ORIGINS[this.channel] && !(this.channel === 'honor' && q.class === 'lifecycle' && endpoint.origin === 'https://iam.developer.honor.com')))
      throw new ChannelError('endpoint_not_allowed', 'not_sent');
    const body = q.body;
    if (q.method === 'GET' && body) throw new ChannelError('GET_body_rejected', 'not_sent');
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), q.class === 'read' ? 30000 : 90000);
    let response: Response;
    try {
      response = await this.fetcher(endpoint, { method: q.method, ...(q.headers ? { headers: q.headers } : {}),
        body: body ? body as BodyInit : null, redirect: 'manual', signal: controller.signal,
        ...(body instanceof Readable ? { duplex: 'half' as const } : {}) });
    } catch { throw new ChannelError('network_or_timeout', q.class === 'read' ? 'not_sent' : 'unknown'); }
    finally { clearTimeout(timer); }
    const successful = response.status >= 200 && response.status <= 299;
    // Token rejection bodies can prove that no token was issued. HTTP status alone cannot.
    if (!successful && !(q.class === 'lifecycle' && [400, 401].includes(response.status)))
      throw new ChannelError('http_status', q.class === 'read' ? 'not_sent' : 'unknown');
    if (q.rawUpload) {
      if (response.status !== 200) throw unknown();
      return {};
    }
    const contentLength = Number(response.headers.get('content-length') ?? 0);
    if (contentLength > 4 * 1024 * 1024) throw unknown();
    const chunks: Uint8Array[] = []; let size = 0;
    if (!response.body) throw unknown();
    for await (const chunk of response.body) {
      size += chunk.byteLength;
      if (size > 4 * 1024 * 1024) throw unknown();
      chunks.push(chunk);
    }
    let parsed: Record<string, unknown>;
    try { parsed = object(parseStrictJSON(Buffer.concat(chunks).toString('utf8'))); }
    catch { throw unknown(); }
    if (!successful) {
      if (tokenRejected(this.channel, parsed)) throw new ChannelError('token_rejected', 'confirmed_rejected');
      throw new ChannelError('http_status');
    }
    return parsed;
  }

  json(path: string, method: 'GET' | 'POST' | 'PUT', kind: RequestClass, value?: unknown, operationId?: string, headers?: Record<string, string>): Promise<Record<string, unknown>> {
    const body = value === undefined ? undefined : Buffer.from(JSON.stringify(value));
    return this.send({ method, path, class: kind, ...(operationId ? { operationId } : {}),
      headers: { 'Content-Type': 'application/json; charset=utf-8', ...headers }, ...(body ? { body, size: body.length } : {}) });
  }
  rawJSON(path: string, method: 'POST' | 'PUT', kind: RequestClass, source: string, operationId?: string, headers?: Record<string, string>): Promise<Record<string, unknown>> {
    parseStrictJSON(source);
    const body = Buffer.from(source);
    return this.send({ method, path, class: kind, ...(operationId ? { operationId } : {}),
      headers: { 'Content-Type': 'application/json; charset=utf-8', ...headers }, body, size: body.length });
  }
  form(path: string, kind: RequestClass, fields: Record<string, string>, operationId?: string, headers?: Record<string, string>): Promise<Record<string, unknown>> {
    const body = Buffer.from(new URLSearchParams(fields).toString());
    return this.send({ method: 'POST', path, class: kind, ...(operationId ? { operationId } : {}),
      headers: { 'Content-Type': 'application/x-www-form-urlencoded; charset=utf-8', ...headers }, body, size: body.length });
  }

  async upload(url: string, file: UploadFile, operationId: string, headers: Record<string, string> = {}): Promise<void> {
    const current = hashFile(file.path);
    if (current.sha256 !== file.sha256 || current.size !== file.size) throw new ChannelError('frozen_apk_changed', 'not_sent');
    await this.send({ method: 'PUT', url, class: 'write', operationId, upload: true, rawUpload: true, body: fs.createReadStream(file.path), size: file.size,
      headers: { 'Content-Type': 'application/octet-stream', 'Content-Length': String(file.size), ...headers } });
  }

  async multipart(endpoint: string, fields: Record<string, string>, files: Record<string, UploadFile>, kind: RequestClass, operationId?: string, headers: Record<string, string> = {}, upload = false): Promise<Record<string, unknown>> {
    const boundary = `publisher-${randomBytes(16).toString('hex')}`;
    const parts: Array<Buffer | UploadFile> = [];
    for (const [key, value] of Object.entries(fields).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)) {
      if (!/^[A-Za-z0-9_]+$/.test(key)) throw new ChannelError('invalid_multipart_field', 'not_sent');
      parts.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${key}"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n${value}\r\n`));
    }
    for (const [key, file] of Object.entries(files).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)) {
      if (!/^[A-Za-z0-9_]+$/.test(key)) throw new ChannelError('invalid_multipart_field', 'not_sent');
      const current = hashFile(file.path);
      if (current.sha256 !== file.sha256 || current.size !== file.size || !/^[A-Za-z0-9._-]+$/.test(file.filename)) throw new ChannelError('invalid_frozen_file', 'not_sent');
      parts.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${key}"; filename="${file.filename}"\r\nContent-Type: application/octet-stream\r\n\r\n`));
      parts.push(file);
      parts.push(Buffer.from('\r\n'));
    }
    parts.push(Buffer.from(`--${boundary}--\r\n`));
    const size = parts.reduce((total, part) => total + (Buffer.isBuffer(part) ? part.length : part.size), 0);
    const body = Readable.from((async function* () {
      for (const part of parts) {
        if (Buffer.isBuffer(part)) yield part;
        else for await (const chunk of fs.createReadStream(part.path)) yield chunk;
      }
    })());
    return this.send({ method: 'POST', ...(endpoint.startsWith('https://') ? { url: endpoint } : { path: endpoint }), class: kind,
      ...(operationId ? { operationId } : {}), upload, body, size,
      headers: { 'Content-Type': `multipart/form-data; boundary=${boundary}`, 'Content-Length': String(size), ...headers } });
  }
}
