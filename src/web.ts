import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { once } from 'node:events';
import { dataRoot, privateDir } from './private-file.js';
import { parseStrictJSON } from './model.js';
import { page } from './web/page.js';
import { clientScript } from './web/client.js';
import { stylesheet } from './web/styles.js';

export interface WebBackend {
  invoke(action: string, payload: unknown): Promise<unknown>;
}

const actions = new Set(['apps.list', 'apps.save', 'apps.check', 'releases.list', 'releases.prepare',
  'releases.preflight', 'releases.review', 'releases.submit', 'releases.get', 'releases.manual', 'releases.reconcile',
  'accounts.list', 'accounts.forget', 'doctor', 'protocols']);
const maxUpload = 1024 * 1024 * 1024;
const maxJSON = 1024 * 1024;
const actionErrors: Record<string, string> = {
  'apps.save': '应用配置未保存。请检查应用名称、包名、各渠道应用标识和小米图标资料。',
  'apps.check': '渠道访问检查未完成。请检查本机凭据状态，并在终端更新需要重新接入的账号。',
  'releases.prepare': '发布材料准备未完成。请检查各渠道 APK 的包名、版本和签名是否一致，更新说明是否符合限制，并查看环境诊断。',
  'releases.preflight': '渠道预检未完成。请检查账号凭据和网络，修复后重新检查。',
  'releases.review': '提交摘要生成未完成。请重新检查渠道，查看是否有待核对请求，并确认本次选定范围。',
  'releases.submit': '提交操作未完成。请先查看本次发布的最新状态，再继续未完成渠道；已受理的渠道会保留结果。',
  'releases.manual': '人工核对记录未保存。请核对渠道、结果来源和核对时间，再重新保存。',
  'releases.reconcile': '远端核对未完成。请保留本次记录，检查网络和账号权限后重试。',
  'accounts.forget': '本机凭据清除未完成。请解锁钥匙串，或在终端使用 account forget 重试。',
};

class RequestError extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}

function headers(response: http.ServerResponse): void {
  response.setHeader('Cache-Control', 'no-store');
  response.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'");
  response.setHeader('X-Content-Type-Options', 'nosniff');
  response.setHeader('X-Frame-Options', 'DENY');
  response.setHeader('Referrer-Policy', 'no-referrer');
  response.setHeader('Cross-Origin-Resource-Policy', 'same-origin');
}

function json(response: http.ServerResponse, status: number, value: unknown): void {
  response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' });
  response.end(JSON.stringify(value));
}

async function readJSONBody(request: http.IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    size += bytes.length;
    if (size > maxJSON) throw new RequestError(413, '请求内容过大。');
    chunks.push(bytes);
  }
  try { return parseStrictJSON(Buffer.concat(chunks).toString('utf8')); }
  catch { throw new RequestError(400, '请求 JSON 格式无效。'); }
}

async function upload(request: http.IncomingMessage, directory: string, icon = false): Promise<{ path: string; filename: string }> {
  const encoded = request.headers['x-filename'];
  if (typeof encoded !== 'string') throw new RequestError(400, '请选择一个文件。');
  let filename: string;
  try { filename = decodeURIComponent(encoded); }
  catch { throw new RequestError(400, '文件名无效。'); }
  if (!filename || filename.length > 255 || filename !== path.basename(filename) || /[\\/\u0000-\u001f\u007f]/u.test(filename) || filename === '.' || filename === '..')
    throw new RequestError(400, '文件名无效。');
  if (icon && !/\.(png|jpe?g)$/iu.test(filename)) throw new RequestError(400, '应用图标须为 PNG 或 JPEG 文件。');
  const limit = icon ? 10 * 1024 * 1024 : maxUpload;
  const sizeError = icon ? '应用图标超过 10 MiB。' : '文件超过 1 GiB。';
  const statedLength = Number(request.headers['content-length']);
  if (Number.isFinite(statedLength) && statedLength > limit) throw new RequestError(413, sizeError);
  const folder = path.join(directory, crypto.randomUUID());
  privateDir(folder);
  const file = path.join(folder, filename);
  const output = fs.createWriteStream(file, { flags: 'wx', mode: 0o600 });
  let size = 0;
  // Observe stream errors before the first write to avoid an unhandled error event.
  let writeError: Error | undefined;
  output.on('error', error => { writeError = error; });
  try {
    for await (const chunk of request) {
      const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
      size += bytes.length;
      if (size > limit) throw new RequestError(413, sizeError);
      if (writeError) throw writeError;
      if (!output.write(bytes)) await once(output, 'drain');
    }
    if (!size) throw new RequestError(400, '文件为空，请重新选择。');
    output.end();
    await once(output, 'finish');
    if (writeError) throw writeError;
    const descriptor = fs.openSync(file, 'r');
    try { fs.fsyncSync(descriptor); } finally { fs.closeSync(descriptor); }
    return { path: file, filename };
  } catch (error) {
    output.destroy();
    await new Promise<void>(resolve => {
      if (output.closed) resolve(); else output.once('close', resolve);
    });
    fs.rmSync(folder, { recursive: true, force: true });
    throw error;
  }
}

/** A loopback-only UI. The injected backend is shared with the CLI. */
export async function startWebServer(backend: WebBackend, options: { port?: number; root?: string } = {}): Promise<{
  url: string;
  server: http.Server;
  close(): Promise<void>;
}> {
  const token = crypto.randomBytes(32).toString('base64url');
  const root = options.root ?? dataRoot();
  const uploads = path.join(root, 'web-uploads', crypto.randomUUID());
  privateDir(uploads);
  let origin = '';
  let expectedHost = '';
  let queue: Promise<void> = Promise.resolve();
  let closed = false;
  const serial = <T>(work: () => Promise<T>): Promise<T> => {
    const result = queue.then(work);
    queue = result.then(() => undefined, () => undefined);
    return result;
  };
  const server = http.createServer((request, response) => {
    headers(response);
    let activeAction = '';
    void (async () => {
      if (request.headers.host !== expectedHost) throw new RequestError(403, '仅允许从本机页面访问。');
      if (request.headers['sec-fetch-site'] === 'cross-site') throw new RequestError(403, '不允许跨站访问。');
      const requested = new URL(request.url ?? '/', origin);
      if (request.method === 'GET') {
        if (request.headers.origin && request.headers.origin !== origin) throw new RequestError(403, '不允许跨站访问。');
        const assets: Record<string, { type: string; body: string }> = {
          '/': { type: 'text/html; charset=utf-8', body: page(token) },
          '/app.js': { type: 'text/javascript; charset=utf-8', body: clientScript },
          '/app.css': { type: 'text/css; charset=utf-8', body: stylesheet },
        };
        const asset = assets[requested.pathname];
        if (!asset) throw new RequestError(404, '页面不存在。');
        response.writeHead(200, { 'Content-Type': asset.type });
        response.end(asset.body);
        return;
      }
      if (request.method !== 'POST') throw new RequestError(405, '不支持此请求方法。');
      if (request.headers.origin !== origin || request.headers['x-publisher-token'] !== token)
        throw new RequestError(403, '页面会话已失效，请刷新页面。');
      if (requested.pathname === '/api/upload') {
        if (request.headers['content-type'] !== 'application/octet-stream') throw new RequestError(415, '上传格式无效。');
        const purpose = request.headers['x-upload-purpose'];
        if (purpose !== undefined && purpose !== 'app-icon') throw new RequestError(400, '文件用途无效。');
        const icon = purpose === 'app-icon';
        const uploaded = await serial(() => upload(request, icon ? path.join(root, 'web-assets', 'icons') : uploads, icon));
        json(response, 201, uploaded);
        return;
      }
      if (requested.pathname !== '/api/action') throw new RequestError(404, '接口不存在。');
      if (!request.headers['content-type']?.startsWith('application/json')) throw new RequestError(415, '请使用 JSON 请求。');
      const body = await readJSONBody(request);
      if (!body || typeof body !== 'object' || Array.isArray(body)) throw new RequestError(400, '请求格式无效。');
      const record = body as Record<string, unknown>;
      if (Object.keys(record).some(key => !['action', 'payload'].includes(key)) || typeof record.action !== 'string' || !actions.has(record.action))
        throw new RequestError(400, '操作无效。');
      const action = record.action;
      activeAction = action;
      const value = await serial(() => backend.invoke(action, record.payload ?? {}));
      json(response, 200, value);
    })().catch(error => {
      if (response.headersSent) { response.destroy(); return; }
      // Vendor responses and arbitrary backend exceptions must not enter browser errors.
      json(response, error instanceof RequestError ? error.status : 400,
        { error: error instanceof RequestError ? error.message : actionErrors[activeAction] ?? '操作未完成。请检查输入、账号凭据和环境诊断后重试。' });
      request.resume();
    });
  });
  server.headersTimeout = 15_000;
  server.requestTimeout = 10 * 60_000;
  server.keepAliveTimeout = 5_000;
  try {
    server.listen(options.port ?? 0, '127.0.0.1');
    await once(server, 'listening');
  } catch (error) {
    fs.rmSync(uploads, { recursive: true, force: true });
    throw error;
  }
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('local web listener is unavailable');
  origin = 'http://127.0.0.1:' + address.port;
  expectedHost = '127.0.0.1:' + address.port;
  return {
    url: origin,
    server,
    async close() {
      if (closed) return;
      closed = true;
      await new Promise<void>((resolve, reject) => {
        server.close(error => { if (error) reject(error); else resolve(); });
        server.closeIdleConnections();
      });
      await queue;
      fs.rmSync(uploads, { recursive: true, force: true });
    },
  };
}
