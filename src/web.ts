import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { once } from 'node:events';
import { dataRoot, privateDir } from './private-file.js';
import { parseStrictJSON } from './model.js';
import { CredentialInputError } from './credentials.js';
import { page } from './web/page.js';
import { clientScript } from './web/client.js';
import { stylesheet } from './web/styles.js';
import { WebWorkspace } from './web/workspace.js';
import { WebJobs } from './web/jobs.js';

export interface WebBackend {
  invoke(action: string, payload: unknown): Promise<unknown>;
}

const actions = new Set(['apps.list', 'apps.save', 'apps.check', 'releases.list', 'releases.prepare',
  'releases.preflight', 'releases.review', 'releases.submit', 'releases.get', 'releases.manual', 'releases.reconcile',
  'accounts.list', 'accounts.update', 'accounts.forget', 'doctor', 'doctor.repair', 'protocols',
  'drafts.get', 'drafts.save', 'drafts.clear', 'jobs.start', 'jobs.get', 'jobs.list']);
const maxUpload = 1024 * 1024 * 1024;
const maxJSON = 1024 * 1024;
const actionErrors: Record<string, string> = {
  'apps.save': '应用资料未保存。请检查名称、Android 包名和各渠道的商店应用资料。已填写内容保留在本机草稿中。',
  'apps.check': '渠道访问验证未完成。应用资料已保留，请检查该渠道的账号、商店应用资料或网络后重新验证。',
  'releases.prepare': '发布材料准备未完成。请检查各渠道 APK 的包名、版本和签名是否一致，更新说明是否符合限制，并查看“本机检查”。',
  'releases.preflight': '渠道检查未完成。APK 和更新说明已保留，尚未提交。请检查账号访问权限或网络后重新检查。',
  'releases.review': '提交内容尚不能确认。请处理未通过的渠道检查或提交结果待确认事项，再重新检查。',
  'releases.submit': '提交未能完成。请先查看本次发布进度，再按各渠道的处理入口操作；商店已接收的结果会保留。',
  'releases.get': '本次查询未能更新结果。已保存的进度仍保留，请检查网络或渠道账号权限后重新查询。',
  'releases.manual': '人工核对记录未保存。请检查渠道、核对来源和核对时间，再重新保存。',
  'releases.reconcile': '提交结果检查未完成。原请求记录已保留，请检查网络或渠道账号权限后重新检查。',
  'accounts.forget': '本机凭据清除未完成。请解锁钥匙串后重试，原发布记录和请求回执仍保留。',
  'accounts.update': '凭据更新未完成。请核对材料、账号权限、网络与钥匙串状态，并查看当前账号状态；存在未决令牌请求时，须先核对原请求。',
  'doctor.repair': '检查组件安装或修复未完成。请检查网络和本机目录权限后重试。',
  'drafts.get': '本机草稿未能读取。请保留现有资料，检查本机目录权限。',
  'drafts.save': '草稿尚未保存。请保留当前页面，检查本机目录权限后重试。',
  'drafts.clear': '本机草稿未能清除。已提交的发布记录仍保留。',
  'jobs.start': '操作未能开始。请先查看当前任务，再重试。',
  'jobs.get': '未找到此操作。请返回应用查看已保存的发布进度。',
};

function safeActionError(action: string, error: unknown): string {
  if (action === 'apps.save' && error instanceof Error) {
    if (error.message === '此 API 账号已接入，请选择已有渠道账号。') return '此 API 账号已接入，请在“渠道账号”中选择已有账号，沿用同一标识。';
    if (error.message === '这个渠道账号的 API 标识保持不变；使用另一个 API 客户端时，请接入新的渠道账号。') return '这个渠道账号已关联原 API 客户端。要使用另一个客户端，请选择“接入新的渠道账号”。';
  }
  if (action === 'releases.prepare' && error instanceof Error) {
    if (error.message === 'all APKs must have the same package, version and signer') return '各渠道 APK 的包名、版本或签名不一致。请在“准备材料”中更换安装包后重新检查。';
    if (error.message === 'APK 包名与应用配置不一致') return 'APK 的 Android 包名与此应用不符。请确认应用选择和各渠道安装包。';
    if (error.message === 'debuggable APK cannot be published') return '选择的 APK 是调试包。请使用已签名的正式发布安装包。';
    if (/^更新说明去掉首尾空白/.test(error.message)) return '更新说明至少需要 5 个字符，并使用常规文本与换行。请修改后重新检查。';
    if (/更新说明最多 (200|500) 个字符/.test(error.message)) return '更新说明超出所选渠道的长度限制。vivo 最多 200 个字符，其他渠道最多 500 个字符。请缩短后重新检查。';
    if (/^(xiaomi|oppo|vivo): configured app ID differs from APK package$/.test(error.message)) return '所选 APK 的 Android 包名与应用资料不符。请确认应用选择和各渠道安装包。';
  }
  return actionErrors[action] ?? '操作未完成。请查看应用当前状态和处理入口后重试。';
}

function safeResult(value: unknown, action: string): unknown {
  if (Array.isArray(value)) return value.map(item => safeResult(item, action));
  if (!value || typeof value !== 'object') return value;
  return Object.fromEntries(Object.entries(value).map(([key, item]) => {
    if (key === 'errors' && item && typeof item === 'object') return [key,
      Object.fromEntries(Object.keys(item).map(channel => [channel, safeActionError(action, undefined)]))];
    if (key === 'last_error' && item) return [key, '本次查询未能更新结果，请检查账号访问权限或网络后重新查询。'];
    return [key, safeResult(item, action)];
  }));
}

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
  let joined: Buffer | undefined;
  try {
    for await (const chunk of request) {
      const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
      chunks.push(bytes);
      size += bytes.length;
      if (size > maxJSON) throw new RequestError(413, '请求内容过大。');
    }
    joined = Buffer.concat(chunks);
    try { return parseStrictJSON(joined.toString('utf8')); }
    catch { throw new RequestError(400, '请求 JSON 格式无效。'); }
  } finally {
    joined?.fill(0);
    for (const chunk of chunks) chunk.fill(0);
  }
}

function clearCredentialPayload(payload: unknown): void {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return;
  const record = payload as Record<string, unknown>;
  for (const key of ['credentials', 'files']) {
    const value = record[key];
    if (value && typeof value === 'object') for (const field of Object.keys(value)) delete (value as Record<string, unknown>)[field];
    delete record[key];
  }
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
  // Selected APKs remain available to saved drafts across browser/service restarts.
  const uploads = path.join(root, 'web-uploads');
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
  const workspace = new WebWorkspace(root);
  const jobs = new WebJobs(work => serial(work), async (action, payload) => safeResult(await backend.invoke(action, payload), action), safeActionError);
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
        const uploaded = await upload(request, icon ? path.join(root, 'web-assets', 'icons') : uploads, icon);
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
      try {
        const payload = record.payload ?? {};
        if (action.startsWith('drafts.')) {
          if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('invalid draft');
          const p = payload as Record<string, unknown>;
          if (Object.keys(p).some(key => !['kind', 'id', 'value'].includes(key))) throw new Error('invalid draft fields');
          if (action === 'drafts.get') json(response, 200, { draft: workspace.get(p.kind, p.id) });
          else if (action === 'drafts.save') { workspace.save(p.kind, p.id, p.value); json(response, 200, { saved: true }); }
          else { workspace.clear(p.kind, p.id); json(response, 200, { cleared: true }); }
          return;
        }
        if (action.startsWith('jobs.')) {
          const p = payload as Record<string, unknown>;
          if (action === 'jobs.start') json(response, 202, { job: jobs.start(payload) });
          else if (action === 'jobs.get') json(response, 200, { job: jobs.get(p?.id) });
          else json(response, 200, { jobs: jobs.list() });
          return;
        }
        // Reading saved facts must remain possible while a browser-independent job runs.
        const cachedRead = ['apps.list', 'accounts.list', 'releases.list', 'doctor', 'protocols'].includes(action) ||
          action === 'releases.get' && (payload as { refresh?: unknown }).refresh !== true;
        const value = cachedRead ? await backend.invoke(action, payload) : await serial(() => backend.invoke(action, payload));
        if (action === 'accounts.update') {
          if (!value || typeof value !== 'object' || (value as { updated?: unknown }).updated !== true) throw new Error('credential update was not confirmed');
          json(response, 200, { updated: true });
        } else json(response, 200, safeResult(value, action));
      } finally { if (action === 'accounts.update') clearCredentialPayload(record.payload); }
    })().catch(error => {
      if (response.headersSent) { response.destroy(); return; }
      // Vendor responses and arbitrary backend exceptions must not enter browser errors.
      json(response, error instanceof RequestError ? error.status : 400,
        { error: error instanceof RequestError || activeAction === 'accounts.update' && error instanceof CredentialInputError
          ? error.message : safeActionError(activeAction, error),
          ...(activeAction === 'accounts.update' && error instanceof CredentialInputError ? { code: error.code } : {}) });
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
    },
  };
}
