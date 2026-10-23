import { Socket } from 'node:net';
import { collect } from './observer.js';
import { Failure, parseRequest, type Status } from './contract.js';

process.umask(0o077);
let cancel: (reason: Failure) => void = () => {};
const canceled = new Promise<never>((_, reject) => { cancel = reject; });
// A catch is installed before request parsing; EOF can arrive at any point.
void canceled.catch(() => {});
let lease: Socket | undefined;
try {
  // Use libuv's nonblocking pipe handle. fs.read on a live inherited pipe can
  // keep the worker pool blocked after destroy and deadlock parent/child exit.
  lease = new Socket({ fd: 3, readable: true, writable: false });
  lease.on('end', () => cancel(new Failure('canceled')));
  lease.on('error', () => cancel(new Failure('invalid_input')));
  lease.resume();
} catch { cancel(new Failure('invalid_input')); }
process.once('SIGTERM', () => cancel(new Failure('canceled')));
process.once('SIGINT', () => cancel(new Failure('canceled')));
process.stdout.on('error', () => cancel(new Failure('canceled')));
let result: Record<string, unknown>;
try {
  const buffers: Buffer[] = []; let size = 0;
  for await (const chunk of process.stdin) {
    size += (chunk as Buffer).length; if (size > 65536) throw new Failure('invalid_input'); buffers.push(chunk as Buffer);
  }
  const request = parseRequest(new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(buffers)));
  result = await collect(request, canceled);
} catch (error) {
  const status: Status = error instanceof Failure ? error.status : 'evidence_insufficient';
  result = { schema_version: 1, mode: 'simulation', status };
} finally { lease?.destroy(); }
// No stderr, raw errors, source HTML or cookies leave this process.
process.stdout.end(JSON.stringify(result) + '\n');
