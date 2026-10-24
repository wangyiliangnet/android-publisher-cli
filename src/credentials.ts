import fs from 'node:fs';
import tty from 'node:tty';
import { type Channel, assertObject, parseStrictJSON, validText } from './model.js';
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

export class FileCredentials implements CredentialSource {
  private readonly document: Record<string, unknown>;
  constructor(file: string) {
    const input = regularFile(file, 256 * 1024, true).toString('utf8');
    const parsed = parseStrictJSON(input);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('credentials file must be an object');
    this.document = parsed as Record<string, unknown>;
  }
  async forChannel(channel: Channel): Promise<CredentialSet> {
    const value = this.document[channel];
    assertObject(value, credentialFields[channel], channel === 'xiaomi' ? ['test_account_json'] : []);
    for (const field of credentialFields[channel]) if (typeof value[field] !== 'string' || !value[field] || Buffer.byteLength(value[field]) > 64 * 1024 || /[\u0000\u007f]/u.test(value[field])) throw new Error(`${channel}: missing credential field ${field}`);
    if (channel === 'xiaomi' && value.test_account_json !== undefined && (typeof value.test_account_json !== 'string' || Buffer.byteLength(value.test_account_json) > 64 * 1024)) throw new Error('Xiaomi test account input is invalid');
    return value as CredentialSet;
  }
}

function askHidden(label: string): Promise<string> {
  return new Promise((resolve, reject) => {
    let fd: number;
    try { fd = fs.openSync('/dev/tty', 'r+'); }
    catch { reject(new Error('interactive credentials require a terminal; use --credentials-file for automation')); return; }
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
    output(`${label}（输入 @/绝对路径 可从私有文件读取）：`);
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
      const typed = await askHidden(`${channel} ${field}`);
      const value = typed.startsWith('@/') ? regularFile(typed.slice(1), 64 * 1024, true).toString('utf8').trimEnd() : typed;
      if (!value || Buffer.byteLength(value) > 64 * 1024 || /[\u0000\u007f]/u.test(value)) throw new Error(`${channel}: credential input is invalid`);
      credentials[field] = value;
    }
    if (channel === 'xiaomi') {
      const typed = await askHidden('xiaomi test_account_json（可留空）');
      if (typed) credentials.test_account_json = typed.startsWith('@/')
        ? regularFile(typed.slice(1), 64 * 1024, true).toString('utf8').trimEnd() : typed;
    }
    this.cache.set(channel, credentials);
    return credentials;
  }
  clear(): void { this.cache.clear(); }
}
