import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { deflateRawSync } from 'node:zlib';
import { apkEntry } from '../build/apk-metadata.js';

function zip(entries) {
  const local = [], central = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from('AndroidManifest.xml'), body = entry.data;
    const head = Buffer.alloc(30); head.writeUInt32LE(0x04034b50); head.writeUInt16LE(entry.method ?? 0, 8); head.writeUInt16LE(name.length, 26);
    head.writeUInt32LE(body.length, 18); head.writeUInt32LE(entry.expanded ?? body.length, 22);
    const cd = Buffer.alloc(46); cd.writeUInt32LE(0x02014b50); cd.writeUInt16LE(entry.method ?? 0, 10); cd.writeUInt32LE(body.length, 20); cd.writeUInt32LE(entry.expanded ?? body.length, 24); cd.writeUInt16LE(name.length, 28); cd.writeUInt32LE(offset, 42);
    local.push(head, name, body); central.push(cd, name); offset += head.length+name.length+body.length;
  }
  const cd = Buffer.concat(central), end = Buffer.alloc(22); end.writeUInt32LE(0x06054b50); end.writeUInt16LE(entries.length, 8); end.writeUInt16LE(entries.length, 10); end.writeUInt32LE(cd.length, 12); end.writeUInt32LE(offset, 16);
  return Buffer.concat([...local, cd, end]);
}
test('APK metadata rejects duplicate ZIP identity and bounded decompression bombs', t => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-zip-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const file = path.join(root, 'synthetic.apk');
  fs.writeFileSync(file, zip([{ data: Buffer.from('first') }, { data: Buffer.from('second') }]));
  assert.throws(() => apkEntry(file, 'AndroidManifest.xml'), /duplicate/);
  fs.writeFileSync(file, zip([{ data: deflateRawSync(Buffer.alloc(17*1024*1024)), method: 8, expanded: 1 }]));
  assert.throws(() => apkEntry(file, 'AndroidManifest.xml'));
  fs.writeFileSync(file, zip([{ data: Buffer.from('single') }]));
  assert.equal(apkEntry(file, 'AndroidManifest.xml').toString(), 'single');
  const corrupt = fs.readFileSync(file); corrupt.writeUInt32LE(0xffffffff, corrupt.length-6); fs.writeFileSync(file, corrupt);
  assert.throws(() => apkEntry(file, 'AndroidManifest.xml'), /directory/);
});
