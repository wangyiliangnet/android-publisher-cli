import fs from 'node:fs';
import path from 'node:path';
import { createCipheriv, createDecipheriv, randomBytes, timingSafeEqual } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { type Channel, digest, isChannel, validAbsolute, validId } from './model.js';
import { type CredentialSet, type CredentialSource, validateCredentials } from './credentials.js';
import { readJSON, writePrivate } from './private-file.js';

export interface KeyStore {
  get(id: string): Buffer | undefined;
  put(id: string, key: Buffer): void;
  delete(id: string): void;
}

/** Only a short encryption key enters security's stdin. Credential contents never enter argv. */
export class MacKeyStore implements KeyStore {
  constructor(private readonly run = spawnSync, private readonly keychain?: string) {
    if (keychain !== undefined && !validAbsolute(keychain)) throw new Error('invalid keychain path');
  }
  get(id: string): Buffer | undefined {
    if (!/^[a-f0-9-]{1,160}$/.test(id)) throw new Error('invalid keychain item');
    const result = this.run('/usr/bin/security', ['find-generic-password', '-s', 'AndroidPublisherCLI', '-a', id, '-w', ...(this.keychain ? [this.keychain] : [])],
      { encoding: 'utf8', timeout: 30000, maxBuffer: 4096 });
    if (result.status === 44) return undefined;
    if (result.status !== 0 || result.error || !/^[a-f0-9]{64}\s*$/.test(result.stdout ?? ''))
      throw new Error('无法读取 macOS 钥匙串，请解锁登录钥匙串后重试');
    return Buffer.from(result.stdout.trim(), 'hex');
  }
  put(id: string, key: Buffer): void {
    if (!/^[a-f0-9-]{1,160}$/.test(id) || key.length !== 32) throw new Error('invalid keychain item');
    const result = this.run('/usr/bin/security', ['-i'], { encoding: 'utf8', timeout: 30000, maxBuffer: 4096,
      input: `add-generic-password -s AndroidPublisherCLI -a ${id} -w ${key.toString('hex')}${this.keychain ? ` "${this.keychain.replace(/["\\]/g, '\\$&')}"` : ''}\n` });
    if (result.status !== 0 || result.error) throw new Error('无法保存到 macOS 钥匙串');
    const saved = this.get(id);
    if (!saved || !timingSafeEqual(saved, key)) throw new Error('macOS 钥匙串保存未确认');
  }
  delete(id: string): void {
    if (!/^[a-f0-9-]{1,160}$/.test(id)) throw new Error('invalid keychain item');
    const result = this.run('/usr/bin/security', ['delete-generic-password', '-s', 'AndroidPublisherCLI', '-a', id, ...(this.keychain ? [this.keychain] : [])],
      { encoding: 'utf8', timeout: 30000, maxBuffer: 4096 });
    if (result.status !== 0 && result.status !== 44) throw new Error('无法清除 macOS 钥匙串中的凭据');
  }
}

export class CredentialVault {
  constructor(readonly root: string, private readonly keys: KeyStore = new MacKeyStore()) {}
  private file(channel: Channel, account: string): string {
    if (!isChannel(channel) || !validId(account)) throw new Error('invalid account');
    return path.join(this.root, 'credentials', `${digest([channel, account])}.json`);
  }
  has(channel: Channel, account: string): boolean { return fs.existsSync(this.file(channel, account)); }
  get(channel: Channel, account: string): CredentialSet | undefined {
    const file = this.file(channel, account);
    if (!fs.existsSync(file)) return undefined;
    try {
      const box = readJSON(file, 1024 * 1024, true) as Record<string, string>;
      if (!box || !/^[a-f0-9-]{1,160}$/.test(box.key_id ?? '')) throw new Error('bad box');
      const key = this.keys.get(box.key_id!);
      if (!key) throw new Error('missing key');
      try {
        const decipher = createDecipheriv('aes-256-gcm', key, Buffer.from(box.iv!, 'base64'));
        decipher.setAAD(Buffer.from(digest([channel, account])));
        decipher.setAuthTag(Buffer.from(box.tag!, 'base64'));
        const plaintext = Buffer.concat([decipher.update(Buffer.from(box.content!, 'base64')), decipher.final()]);
        try { return validateCredentials(channel, JSON.parse(plaintext.toString('utf8'))); }
        finally { plaintext.fill(0); }
      } finally { key.fill(0); }
    } catch { throw new Error(`${channel}：凭据无法解密，请在本地 Web 的“账号与凭据”中更新，或运行 android-release account update ${channel} ${account}`); }
  }
  save(channel: Channel, account: string, credentials: CredentialSet): void {
    validateCredentials(channel, credentials);
    const file = this.file(channel, account);
    const previous = fs.existsSync(file) ? readJSON(file, 1024 * 1024, true) as { key_id: string } : undefined;
    const key = randomBytes(32);
    const keyId = `${digest([this.root, channel, account])}-${randomBytes(12).toString('hex')}`;
    this.keys.put(keyId, key);
    try {
      const iv = randomBytes(12);
      const cipher = createCipheriv('aes-256-gcm', key, iv);
      cipher.setAAD(Buffer.from(digest([channel, account])));
      const content = Buffer.concat([cipher.update(JSON.stringify(credentials), 'utf8'), cipher.final()]);
      writePrivate(file, JSON.stringify({ key_id: keyId, iv: iv.toString('base64'),
        tag: cipher.getAuthTag().toString('base64'), content: content.toString('base64') }) + '\n', true, true);
    } catch (error) { this.keys.delete(keyId); throw error; }
    finally { key.fill(0); }
    if (previous?.key_id) this.keys.delete(previous.key_id);
  }
  forget(channel: Channel, account: string): void {
    const file = this.file(channel, account);
    if (!fs.existsSync(file)) return;
    const box = readJSON(file, 1024 * 1024, true) as { key_id: string };
    this.keys.delete(box.key_id);
    fs.unlinkSync(file);
  }
  source(accounts: Array<{ channel: Channel; account: string }>): CredentialSource {
    return { forChannel: async channel => {
      const account = accounts.find(row => row.channel === channel)?.account;
      const saved = account ? this.get(channel, account) : undefined;
      if (!saved) throw new Error(`${channel}：请先在本地 Web 的“账号与凭据”中接入，或运行 android-release account update ${channel}${account ? ` ${account}` : ''}`);
      return saved;
    } };
  }
}
