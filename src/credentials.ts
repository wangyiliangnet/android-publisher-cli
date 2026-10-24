import fs from 'node:fs';
import tty from 'node:tty';
import { type Channel } from './model.js';
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
      const typed = await askHidden(`${channel} ${field}${fileInput ? ' 文件绝对路径' : ''}`);
      const value = fileInput ? regularFile(typed, 64 * 1024, true).toString('utf8').trimEnd() : typed;
      if (!value || Buffer.byteLength(value) > 64 * 1024 || /[\u0000\u007f]/u.test(value)) throw new Error(`${channel}: credential input is invalid`);
      credentials[field] = value;
    }
    if (channel === 'xiaomi') {
      const file = await askHidden('xiaomi test_account_json 文件绝对路径（可留空）');
      if (file) {
        const value = regularFile(file, 64 * 1024, true).toString('utf8').trimEnd();
        if (!value) throw new Error('xiaomi: test account input is empty');
        credentials.test_account_json = value;
      }
    }
    this.cache.set(channel, credentials);
    return credentials;
  }
  clear(): void { this.cache.clear(); }
}
