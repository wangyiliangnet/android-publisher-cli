import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { parseStrictJSON } from './model.js';

export const dataRoot = (): string => path.join(os.homedir(), 'Library', 'Application Support', 'AndroidPublisherCLI', 'v2');

function noSymlinkAncestors(file: string): void {
  let current = path.dirname(file);
  while (current !== path.dirname(current)) {
    const st = fs.lstatSync(current);
    if (st.isSymbolicLink() || !st.isDirectory()) throw new Error('file path contains a symbolic link');
    current = path.dirname(current);
  }
}

export function privateDir(dir: string): void {
  if (!path.isAbsolute(dir) || path.normalize(dir) !== dir) throw new Error('directory must be an absolute normalized path');
  const parts = dir.split(path.sep).filter(Boolean);
  let current: string = path.sep;
  for (const part of parts) {
    current = path.join(current, part);
    if (!fs.existsSync(current)) fs.mkdirSync(current, { mode: 0o700 });
    const st = fs.lstatSync(current);
    if (!st.isDirectory() || st.isSymbolicLink()) throw new Error('directory contains a symbolic link');
  }
  fs.chmodSync(dir, 0o700);
}

export function regularFile(file: string, maxBytes: number, privateOnly = false): Buffer {
  if (!path.isAbsolute(file) || path.normalize(file) !== file) throw new Error('file path must be absolute and normalized');
  noSymlinkAncestors(file);
  const st = fs.lstatSync(file);
  if (!st.isFile() || st.isSymbolicLink() || st.size > maxBytes || st.size < 0) throw new Error('file is not an allowed regular file');
  if (privateOnly && ((st.mode & 0o7777) !== 0o600 || st.uid !== process.getuid?.())) throw new Error('file must belong to its owner and have mode 0600');
  const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  try {
    const opened = fs.fstatSync(fd);
    if (!opened.isFile() || opened.ino !== st.ino || opened.dev !== st.dev || opened.size > maxBytes) throw new Error('file changed while opening');
    return fs.readFileSync(fd);
  } finally { fs.closeSync(fd); }
}

export function readJSON(file: string, maxBytes = 1024 * 1024, privateOnly = false): unknown {
  return parseStrictJSON(regularFile(file, maxBytes, privateOnly).toString('utf8'));
}

export function writePrivate(file: string, content: string | Buffer, replace = false, createParents = false): void {
  if (!path.isAbsolute(file) || path.normalize(file) !== file) throw new Error('output file path must be absolute');
  if (createParents) privateDir(path.dirname(file));
  else noSymlinkAncestors(file);
  if (!replace && fs.existsSync(file)) throw new Error('file exists; choose a new path');
  const temp = path.join(path.dirname(file), `.tmp-${process.pid}-${Date.now()}-${Math.random().toString(16).slice(2)}`);
  const fd = fs.openSync(temp, 'wx', 0o600);
  try {
    fs.writeFileSync(fd, content);
    fs.fsyncSync(fd);
  } finally { fs.closeSync(fd); }
  try {
    if (replace) {
      if (fs.existsSync(file) && !fs.lstatSync(file).isFile()) throw new Error('destination is not a regular file');
      fs.renameSync(temp, file);
    } else {
      fs.linkSync(temp, file);
      fs.unlinkSync(temp);
    }
    const dir = fs.openSync(path.dirname(file), 'r');
    try { fs.fsyncSync(dir); } finally { fs.closeSync(dir); }
  } finally { if (fs.existsSync(temp)) fs.unlinkSync(temp); }
}

export function hashFile(file: string, maxBytes = 2 * 1024 * 1024 * 1024): { sha256: string; size: number } {
  const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  try {
    const st = fs.fstatSync(fd);
    if (!st.isFile() || st.size <= 0 || st.size > maxBytes) throw new Error('file size or type is invalid');
    const h = createHash('sha256');
    const chunk = Buffer.allocUnsafe(1024 * 1024);
    let pos = 0;
    while (pos < st.size) {
      const count = fs.readSync(fd, chunk, 0, Math.min(chunk.length, st.size - pos), pos);
      if (count <= 0) throw new Error('file changed while hashing');
      h.update(chunk.subarray(0, count)); pos += count;
    }
    if (fs.fstatSync(fd).size !== st.size) throw new Error('file changed while hashing');
    return { sha256: h.digest('hex'), size: st.size };
  } finally { fs.closeSync(fd); }
}

export function md5File(file: string, expectedSHA256: string): string {
  if (hashFile(file).sha256 !== expectedSHA256) throw new Error('frozen file changed');
  const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  try {
    const st = fs.fstatSync(fd); const h = createHash('md5'); const chunk = Buffer.allocUnsafe(1024 * 1024);
    let pos = 0;
    while (pos < st.size) {
      const count = fs.readSync(fd, chunk, 0, Math.min(chunk.length, st.size - pos), pos);
      if (count <= 0) throw new Error('frozen file changed');
      h.update(chunk.subarray(0, count)); pos += count;
    }
    return h.digest('hex');
  } finally { fs.closeSync(fd); }
}

export function freezeFile(source: string, destination: string, expectedHash: string): void {
  privateDir(path.dirname(destination));
  if (fs.existsSync(destination)) {
    if (hashFile(destination).sha256 !== expectedHash) throw new Error('frozen file differs from its saved hash');
    return;
  }
  const temp = path.join(path.dirname(destination), `.freeze-${process.pid}-${Date.now()}`);
  const src = fs.openSync(source, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
  const dst = fs.openSync(temp, 'wx', 0o600);
  try {
    const st = fs.fstatSync(src);
    if (!st.isFile()) throw new Error('source is not a regular file');
    const h = createHash('sha256');
    const chunk = Buffer.allocUnsafe(1024 * 1024);
    let pos = 0;
    while (pos < st.size) {
      const count = fs.readSync(src, chunk, 0, Math.min(chunk.length, st.size - pos), pos);
      if (count <= 0) throw new Error('source changed while freezing');
      h.update(chunk.subarray(0, count));
      let written = 0;
      while (written < count) written += fs.writeSync(dst, chunk, written, count - written);
      pos += count;
    }
    fs.fsyncSync(dst);
    if (h.digest('hex') !== expectedHash || fs.fstatSync(src).size !== st.size) throw new Error('source changed while freezing');
  } catch (e) {
    fs.closeSync(src); fs.closeSync(dst); fs.unlinkSync(temp); throw e;
  }
  fs.closeSync(src); fs.closeSync(dst);
  try {
    fs.linkSync(temp, destination);
    const dir = fs.openSync(path.dirname(destination), 'r');
    try { fs.fsyncSync(dir); } finally { fs.closeSync(dir); }
  } finally { fs.unlinkSync(temp); }
}
