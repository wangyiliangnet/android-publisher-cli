import fs from 'node:fs';
import { inflateRawSync } from 'node:zlib';

/** Read bounded ZIP entries, rejecting duplicate names and conflicting local headers. */
export function apkEntry(file: string, wanted: string): Buffer {
  const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  try {
    const size = fs.fstatSync(fd).size;
    const read = (offset: number, length: number): Buffer => {
      if (offset < 0 || length < 0 || offset + length > size) throw new Error('invalid APK ZIP bounds');
      const bytes = Buffer.alloc(length);
      if (fs.readSync(fd, bytes, 0, length, offset) !== length) throw new Error('truncated APK ZIP');
      return bytes;
    };
    const tail = read(Math.max(0, size - 65557), Math.min(size, 65557));
    let end = -1;
    for (let i = tail.length - 22; i >= 0; i--) if (tail.readUInt32LE(i) === 0x06054b50 &&
      i + 22 + tail.readUInt16LE(i + 20) === tail.length) { end = i; break; }
    if (end < 0 || tail.readUInt16LE(end + 4) || tail.readUInt16LE(end + 6)) throw new Error('invalid APK ZIP directory');
    const count = tail.readUInt16LE(end + 10);
    const directory = tail.readUInt32LE(end + 16);
    const directorySize = tail.readUInt32LE(end + 12);
    if (count === 65535 || directory + directorySize > size - tail.length + end) throw new Error('unsupported APK ZIP directory');
    let pos = directory;
    let found: Buffer | undefined;
    for (let i = 0; i < count; i++) {
      const head = read(pos, 46);
      if (head.readUInt32LE(0) !== 0x02014b50) throw new Error('invalid APK ZIP entry');
      const nameLength = head.readUInt16LE(28);
      if (nameLength > 4096) throw new Error('APK ZIP filename is too long');
      const name = read(pos + 46, nameLength).toString('utf8');
      pos += 46 + nameLength + head.readUInt16LE(30) + head.readUInt16LE(32);
      if (pos > directory + directorySize) throw new Error('invalid APK ZIP directory size');
      if (name !== wanted) continue;
      if (found) throw new Error('duplicate APK metadata entry');
      const compressed = head.readUInt32LE(20);
      const expanded = head.readUInt32LE(24);
      if (compressed > 16 * 1024 * 1024 || expanded > 16 * 1024 * 1024 || head.readUInt16LE(8) & 1)
        throw new Error('APK metadata size or encryption is unsupported');
      const offset = head.readUInt32LE(42);
      const local = read(offset, 30);
      if (local.readUInt32LE(0) !== 0x04034b50 || local.readUInt16LE(8) !== head.readUInt16LE(10) ||
        read(offset + 30, local.readUInt16LE(26)).toString('utf8') !== wanted) throw new Error('conflicting APK ZIP header');
      const dataOffset = offset + 30 + local.readUInt16LE(26) + local.readUInt16LE(28);
      if (dataOffset + compressed > directory) throw new Error('invalid APK entry bounds');
      const data = read(dataOffset, compressed);
      const method = head.readUInt16LE(10);
      if (method !== 0 && method !== 8) throw new Error('unsupported APK compression');
      found = method === 0 ? data : inflateRawSync(data, { maxOutputLength: 16 * 1024 * 1024 });
      if (found.length !== expanded) throw new Error('invalid APK metadata length');
    }
    if (!found) throw new Error(`APK is missing ${wanted}`);
    return found;
  } finally { fs.closeSync(fd); }
}

type Chunk = { offset: number; type: number; header: number; size: number };
function chunks(data: Buffer, start: number, end: number): Chunk[] {
  const result: Chunk[] = [];
  for (let pos = start; pos < end;) {
    if (pos + 8 > end) throw new Error('truncated Android resource chunk');
    const type = data.readUInt16LE(pos), header = data.readUInt16LE(pos + 2), size = data.readUInt32LE(pos + 4);
    if (header < 8 || size < header || pos + size > end) throw new Error('invalid Android resource chunk');
    result.push({ offset: pos, type, header, size }); pos += size;
  }
  return result;
}

function strings(data: Buffer, chunk: Chunk): string[] {
  const { offset: o, size, header } = chunk;
  if (header < 28) throw new Error('invalid Android string pool');
  const count = data.readUInt32LE(o + 8), flags = data.readUInt32LE(o + 16), start = data.readUInt32LE(o + 20);
  if (count > 500000 || header + count * 4 > size || start < header + count * 4 || start > size) throw new Error('invalid Android string pool');
  const end = o + size;
  return Array.from({ length: count }, (_, i) => {
    let p = o + start + data.readUInt32LE(o + header + i * 4);
    const length = (utf8: boolean): number => {
      if (p + (utf8 ? 1 : 2) > end) throw new Error('truncated Android string');
      const first = utf8 ? data[p++]! : data.readUInt16LE((p += 2) - 2);
      if (utf8 && first & 128) { if (p >= end) throw new Error('truncated Android string'); return ((first & 127) << 8) | data[p++]!; }
      if (!utf8 && first & 32768) { if (p + 2 > end) throw new Error('truncated Android string'); const second = data.readUInt16LE(p); p += 2; return ((first & 32767) * 65536) + second; }
      return first;
    };
    const utf8 = Boolean(flags & 256);
    const chars = length(utf8);
    const bytes = utf8 ? length(true) : chars * 2;
    if (p + bytes + (utf8 ? 1 : 2) > end || data[p + bytes] !== 0 || !utf8 && data[p + bytes + 1] !== 0) throw new Error('invalid Android string');
    return new TextDecoder(utf8 ? 'utf-8' : 'utf-16le', { fatal: true }).decode(data.subarray(p, p + bytes));
  });
}

type Value = { type: number; data: number; pool: string[] };
function resourceValue(file: string, id: number, seen = new Set<number>()): Value {
  if (seen.has(id) || seen.size > 16) throw new Error('cyclic APK resource');
  seen.add(id);
  const data = apkEntry(file, 'resources.arsc');
  const table = chunks(data, 0, data.length)[0];
  if (!table || table.type !== 2) throw new Error('invalid APK resource table');
  const root = chunks(data, table.header, table.size);
  const poolChunk = root.find(c => c.type === 1);
  if (!poolChunk) throw new Error('missing APK resource strings');
  const pool = strings(data, poolChunk);
  for (const pkg of root.filter(c => c.type === 0x0200)) {
    if (data.readUInt32LE(pkg.offset + 8) !== id >>> 24) continue;
    const typeOffset = pkg.header >= 288 ? data.readUInt32LE(pkg.offset + 284) : 0;
    for (const type of chunks(data, pkg.offset + pkg.header, pkg.offset + pkg.size).filter(c => c.type === 0x0201)) {
      const o = type.offset;
      if (type.header < 24 || data[o + 8]! + typeOffset !== ((id >>> 16) & 255)) continue;
      const configSize = data.readUInt32LE(o + 20);
      if (configSize < 4 || 20 + configSize > type.header) throw new Error('invalid resource configuration');
      if (data.subarray(o + 24, o + 20 + configSize).some(byte => byte !== 0)) continue;
      const count = data.readUInt32LE(o + 12), entryStart = data.readUInt32LE(o + 16), index = id & 65535;
      const flags = data[o + 9]!;
      let relative = 0xffffffff;
      if (flags & 1) {
        if (type.header + count * 4 > type.size) throw new Error('invalid sparse resource table');
        for (let n = 0; n < count; n++) if (data.readUInt16LE(o + type.header + n * 4) === index) relative = data.readUInt16LE(o + type.header + n * 4 + 2) * 4;
      } else if (index < count) {
        const width = flags & 2 ? 2 : 4;
        if (type.header + count * width > type.size) throw new Error('invalid resource offsets');
        relative = width === 2 ? data.readUInt16LE(o + type.header + index * 2) : data.readUInt32LE(o + type.header + index * 4);
        if (width === 2) relative = relative === 65535 ? 0xffffffff : relative * 4;
      }
      if (relative === 0xffffffff) continue;
      const entry = o + entryStart + relative;
      if (entry < o + type.header || entry + 16 > o + type.size || data.readUInt16LE(entry + 2) & 9) throw new Error('unsupported APK resource value');
      const value = entry + data.readUInt16LE(entry);
      if (value + 8 > o + type.size || data.readUInt16LE(value) !== 8) throw new Error('invalid APK resource value');
      const found = { type: data[value + 3]!, data: data.readUInt32LE(value + 4), pool };
      return found.type === 1 ? resourceValue(file, found.data, seen) : found;
    }
  }
  throw new Error('APK metadata resource has no default value');
}

export function apkVersionName(file: string): string {
  const data = apkEntry(file, 'AndroidManifest.xml');
  const xml = chunks(data, 0, data.length)[0];
  if (!xml || xml.type !== 3 || xml.header !== 8) throw new Error('invalid binary Android manifest');
  const nodes = chunks(data, xml.header, xml.size);
  const poolChunk = nodes.find(c => c.type === 1);
  if (!poolChunk) throw new Error('missing manifest strings');
  const pool = strings(data, poolChunk);
  let depth = 0;
  let version: string | undefined;
  let manifests = 0;
  for (const c of nodes) {
    if (c.type === 0x0103) { depth--; if (depth < 0) throw new Error('unbalanced manifest'); continue; }
    if (c.type !== 0x0102) continue;
    const o = c.offset;
    if (c.header !== 16 || c.size < 36) throw new Error('invalid manifest element');
    if (depth++ !== 0) continue;
    if (++manifests !== 1 || pool[data.readUInt32LE(o + 20)] !== 'manifest') throw new Error('invalid manifest root');
    const start = 16 + data.readUInt16LE(o + 24), width = data.readUInt16LE(o + 26), count = data.readUInt16LE(o + 28);
    if (width < 20 || start < 36 || start + width * count > c.size) throw new Error('invalid manifest attributes');
    for (let n = 0; n < count; n++) {
      const a = o + start + n * width;
      if (pool[data.readUInt32LE(a)] !== 'http://schemas.android.com/apk/res/android' || pool[data.readUInt32LE(a + 4)] !== 'versionName') continue;
      if (version !== undefined || data.readUInt16LE(a + 12) !== 8) throw new Error('duplicate or invalid versionName');
      let value: Value = { type: data[a + 15]!, data: data.readUInt32LE(a + 16), pool };
      if (value.type === 1) value = resourceValue(file, value.data);
      if (value.type !== 3 || value.pool[value.data] === undefined) throw new Error('APK versionName must be a string');
      version = value.pool[value.data];
    }
  }
  if (depth !== 0 || !version || version.length > 255 || /[\u0000-\u001f\u007f]/.test(version)) throw new Error('invalid APK versionName');
  return version;
}
