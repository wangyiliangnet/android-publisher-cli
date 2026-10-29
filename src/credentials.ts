import fs from 'node:fs';
import tty from 'node:tty';
import { type Channel, isChannel, parseStrictJSON, validAbsolute } from './model.js';
import { regularFile } from './private-file.js';

export type CredentialSet = Record<string, string>;
export interface CredentialSource { forChannel(channel: Channel): Promise<CredentialSet>; }

export const credentialFields: Record<Channel, readonly string[]> = {
  huawei: ['service_account_json'],
  honor: ['client_secret'],
  xiaomi: ['api_secret', 'public_key_pem'],
  oppo: ['client_secret'],
  vivo: ['access_secret'],
  tencent: ['api_secret'],
};

const credentialIssues = {
  input: '凭据输入格式无效，请按所选渠道填写。',
  fields: '凭据字段缺失、重复或不属于该渠道，请检查输入方式。',
  file: '无法读取私有材料。请使用属于当前用户、权限为 0600 的本机绝对文件路径，或直接粘贴材料内容。',
  format: '凭据材料格式无效，请检查密钥及 JSON/PEM 内容。',
  account: '账号尚未关联应用，请先配置应用和渠道账号资料。',
} as const;
export class CredentialInputError extends Error {
  readonly code: keyof typeof credentialIssues;
  constructor(code: keyof typeof credentialIssues) {
    const safe = Object.hasOwn(credentialIssues, code) ? code : 'input';
    super(credentialIssues[safe]); this.code = safe;
  }
}

/** Browser text stays in memory; paths use exactly the CLI's private-file checks. */
export function resolveCredentialInput(channel: Channel, raw: unknown = {}, files: unknown = {}): CredentialSet {
  const object = (value: unknown): value is Record<string, unknown> => Boolean(value && typeof value === 'object' && !Array.isArray(value));
  if (!isChannel(channel) || !object(raw) || !object(files)) throw new CredentialInputError('input');
  const allowed = [...credentialFields[channel], ...(channel === 'xiaomi' ? ['test_account_json'] : [])];
  const material = ['service_account_json', 'public_key_pem', 'test_account_json'];
  const credentials: CredentialSet = {};
  try {
    for (const [field, value] of Object.entries(raw)) {
      if (!allowed.includes(field) || typeof value !== 'string' || Object.hasOwn(files, field)) throw new CredentialInputError('fields');
      credentials[field] = value;
    }
    for (const [field, file] of Object.entries(files)) {
      if (!allowed.includes(field) || !material.includes(field)) throw new CredentialInputError('fields');
      if (!validAbsolute(file)) throw new CredentialInputError('file');
      try {
        const bytes = regularFile(file, 65536, true);
        try { credentials[field] = new TextDecoder('utf-8', { fatal: true }).decode(bytes).trimEnd(); }
        finally { bytes.fill(0); }
      }
      catch { throw new CredentialInputError('file'); }
    }
    try { return validateCredentials(channel, credentials); }
    catch { throw new CredentialInputError('format'); }
  } catch (error) {
    for (const field of Object.keys(credentials)) delete credentials[field];
    throw error;
  }
}

export function validateCredentials(channel: Channel, value: unknown): CredentialSet {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('invalid credential material');
  const fields = credentialFields[channel];
  const values = value as Record<string, unknown>;
  if (!fields || Object.keys(values).some(key => !fields.includes(key) && !(channel === 'xiaomi' && key === 'test_account_json')) ||
    fields.some(key => !values[key])) throw new Error('missing channel credential fields');
  for (const entry of Object.values(values)) if (typeof entry !== 'string' || !entry ||
    Buffer.byteLength(entry) > 65536 || /[\u0000\u007f]/.test(entry)) throw new Error('invalid credential material');
  for (const field of ['service_account_json', 'test_account_json']) if (values[field]) {
    try { parseStrictJSON(values[field] as string); }
    catch { throw new Error(`${channel}：JSON 凭据材料格式无效，请检查私有文件`); }
  }
  return values as CredentialSet;
}

const credentialLabels: Record<Channel, Record<string, string>> = {
  huawei: { service_account_json: '华为服务账号 JSON 文件的绝对路径' },
  honor: { client_secret: '荣耀 client_secret（与配置中的 client_id 配对的密钥）' },
  xiaomi: { api_secret: '小米 API 密钥（api_secret）', public_key_pem: '小米 API 公钥或证书 PEM 文件的绝对路径' },
  oppo: { client_secret: 'OPPO client_secret（与配置中的 client_id 配对的密钥）' },
  vivo: { access_secret: 'vivo access_secret（与配置中的 access_key 配对的密钥）' },
  tencent: { api_secret: '应用宝主开发者账号的 API 密钥（api_secret）' },
};

function askHidden(label: string): Promise<string> {
  return new Promise((resolve, reject) => {
    let fd: number;
    try { fd = fs.openSync('/dev/tty', 'r+'); }
    catch { reject(new Error('credential input requires a terminal')); return; }
    const input = new tty.ReadStream(fd);
    if (!input.isTTY || !input.setRawMode) { input.destroy(); reject(new Error('terminal input unavailable')); return; }
    const output = (value: string) => fs.writeSync(fd, value);
    const bytes: number[] = [];
    let done = false;
    const finish = (error?: Error) => {
      if (done) return; done = true;
      input.setRawMode(false); input.removeAllListeners('data'); input.pause(); output('\n'); input.destroy();
      if (error) reject(error); else resolve(Buffer.from(bytes).toString('utf8'));
      bytes.fill(0);
    };
    output(`${label}：`);
    input.setRawMode(true); input.resume();
    input.on('data', (chunk: Buffer) => {
      for (const byte of chunk) {
        if (byte === 3 || byte === 4) { finish(new Error('credential input canceled')); return; }
        if (byte === 13 || byte === 10) { finish(); return; }
        if (byte === 127 || byte === 8) { bytes.pop(); continue; }
        if (bytes.length >= 64 * 1024) { finish(new Error('credential input is too long')); return; }
        bytes.push(byte);
      }
    });
    input.on('error', () => finish(new Error('credential input failed')));
  });
}

export class PromptCredentials implements CredentialSource {
  private readonly cache = new Map<Channel, CredentialSet>();
  async forChannel(channel: Channel): Promise<CredentialSet> {
    const cached = this.cache.get(channel);
    if (cached) return cached;
    const credentials: CredentialSet = {};
    for (const field of credentialFields[channel]) {
      const fileInput = field === 'service_account_json' || field === 'public_key_pem';
      const typed = await askHidden(credentialLabels[channel][field]!);
      const value = fileInput ? regularFile(typed, 64 * 1024, true).toString('utf8').trimEnd() : typed;
      if (!value || Buffer.byteLength(value) > 64 * 1024 || /[\u0000\u007f]/u.test(value)) throw new Error(`${channel}: credential input is invalid`);
      credentials[field] = value;
    }
    if (channel === 'xiaomi') {
      const file = await askHidden('小米审核登录用的测试账号 JSON 文件绝对路径（test_account_json，不需要时回车跳过）');
      if (file) {
        const value = regularFile(file, 64 * 1024, true).toString('utf8').trimEnd();
        if (!value) throw new Error('xiaomi: test account input is empty');
        credentials.test_account_json = value;
      }
    }
    validateCredentials(channel, credentials);
    this.cache.set(channel, credentials);
    return credentials;
  }
  clear(): void { this.cache.clear(); }
}
