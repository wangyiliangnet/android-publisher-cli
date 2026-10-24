import { Store } from '../../build/state.js';

const [root, url] = process.argv.slice(2);
const store = new Store(root);
const operation = store.beginOperation('crash-batch', 'xiaomi', 'push', 'd'.repeat(64));
await fetch(url, { method: 'POST', body: JSON.stringify({ operation_id: operation.id }) });
process.kill(process.pid, 'SIGKILL');
