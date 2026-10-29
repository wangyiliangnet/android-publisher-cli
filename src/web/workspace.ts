import fs from 'node:fs';
import path from 'node:path';
import { assertObject, isChannel, validAbsolute, validId, validText } from '../model.js';
import { readJSON, writePrivate } from '../private-file.js';

/** Persistent, non-secret form inputs. Credentials never cross this boundary. */
export class WebWorkspace {
  constructor(private readonly root: string) {}
  private file(kind: unknown, id: unknown): string {
    if (kind !== 'app' && kind !== 'release' || !validId(id)) throw new Error('invalid draft identity');
    return path.join(this.root, 'web-drafts', String(kind), `${id}.json`);
  }
  get(kind: unknown, id: unknown): unknown {
    const file = this.file(kind, id);
    if (!fs.existsSync(file)) return null;
    const value = readJSON(file, 512 * 1024, true);
    this.validate(kind, value);
    if (kind === 'release' && (value as { app_id: string }).app_id !== id) throw new Error('draft belongs to another application');
    return value;
  }
  save(kind: unknown, id: unknown, value: unknown): void {
    const file = this.file(kind, id);
    this.validate(kind, value);
    if (kind === 'release' && (value as { app_id: string }).app_id !== id) throw new Error('draft belongs to another application');
    writePrivate(file, JSON.stringify(value) + '\n', true, true);
  }
  clear(kind: unknown, id: unknown): void {
    const file = this.file(kind, id);
    if (fs.existsSync(file)) { readJSON(file, 512 * 1024, true); fs.unlinkSync(file); }
  }
  private validate(kind: unknown, value: unknown): void {
    if (kind === 'app') {
      assertObject(value, [], ['schema_version', 'project_id', 'display_name', 'package_name', 'channels']);
      if (value.schema_version !== undefined && value.schema_version !== 2) throw new Error('invalid draft schema');
      for (const field of ['project_id', 'display_name', 'package_name'])
        if (value[field] !== undefined && !validText(value[field], 256)) throw new Error('invalid draft text');
      if (value.channels !== undefined) {
        if (!Array.isArray(value.channels) || value.channels.length > 6) throw new Error('invalid draft channels');
        const seen = new Set<string>();
        for (const row of value.channels) {
          assertObject(row, ['channel'], ['account', 'app_id', 'client_id', 'language_id', 'icon_path', 'privacy_url']);
          if (!isChannel(row.channel) || seen.has(row.channel)) throw new Error('invalid draft channel');
          seen.add(row.channel);
          for (const [key, item] of Object.entries(row)) if (key !== 'channel' && !validText(item, 4096)) throw new Error('invalid draft field');
        }
      }
    } else {
      assertObject(value, ['app_id', 'channels', 'apks', 'release_notes'], ['release_id']);
      if (!validId(value.app_id) || value.release_id !== undefined && !validId(value.release_id)) throw new Error('invalid draft app');
      if (!Array.isArray(value.channels) || value.channels.some(channel => !isChannel(channel)) || new Set(value.channels).size !== value.channels.length)
        throw new Error('invalid draft channels');
      assertObject(value.apks, [], ['huawei', 'honor', 'xiaomi', 'oppo', 'vivo', 'tencent']);
      for (const file of Object.values(value.apks)) if (!validAbsolute(file)) throw new Error('invalid draft APK');
      if (typeof value.release_notes !== 'string' || Buffer.byteLength(value.release_notes) > 65536 || /[\u0000\u007f]/u.test(value.release_notes)) throw new Error('invalid draft notes');
    }
    if (Buffer.byteLength(JSON.stringify(value)) > 512 * 1024) throw new Error('draft is too large');
  }
}
