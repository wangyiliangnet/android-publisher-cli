import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

test('terminal Ctrl+C and EOF cancel configuration with exit130 and no saved app', { skip: process.platform === 'win32' }, t => {
  const root = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), 'publisher-cli-tty-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const script = `
import os,pty,subprocess,sys,select,time,json,threading
master,slave=pty.openpty()
child=subprocess.Popen([sys.argv[1],sys.argv[2],'app','add','synthetic'],stdin=slave,stdout=slave,stderr=slave,env={**os.environ,'HOME':sys.argv[3]})
os.close(slave)
try:
 output=b'';deadline=time.time()+5
 while time.time()<deadline:
  ready,_,_=select.select([master],[],[],0.1)
  if ready:output+=os.read(master,65536)
  if '要发布到哪些渠道'.encode() in output:break
 assert '要发布到哪些渠道'.encode() in output,'prompt unavailable'
 # Keep reading like a terminal emulator while the child restores its TTY.
 stop=threading.Event()
 def drain():
  global output
  while not stop.is_set():
   if select.select([master],[],[],0.05)[0]:
    try: chunk=os.read(master,65536)
    except OSError: break
    if not chunk:break
    output=(output+chunk)[-65536:]
 reader=threading.Thread(target=drain,daemon=True)
 reader.start()
 os.write(master,bytes([int(sys.argv[4])]))
 try:
  code=child.wait(timeout=5)
 except subprocess.TimeoutExpired:
  raise AssertionError(json.dumps({'control':int(sys.argv[4]),'output':output.decode(errors='replace')[-1600:]},ensure_ascii=False))
 print(json.dumps({'code':code}))
finally:
 if 'stop' in locals():stop.set();reader.join(timeout=1)
 if child.poll() is None:child.kill();child.wait()
 os.close(master)
`;
  for (const control of ['3','4']) {
    const result = spawnSync('python3', ['-c', script, process.execPath, path.resolve('build/cli.js'), root, control], { encoding:'utf8', timeout:15000 });
    assert.equal(result.status,0,result.stderr);
    assert.equal(JSON.parse(result.stdout).code,130);
  }
  assert.equal(fs.existsSync(path.join(root,'Library','Application Support','AndroidPublisherCLI','v2','projects','synthetic.json')),false);
});
