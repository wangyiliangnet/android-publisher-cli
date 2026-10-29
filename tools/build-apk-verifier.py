#!/usr/bin/env python3
"""Rebuild the bundled verifier entry point using an apksig classpath."""
import argparse
import base64
import hashlib
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--classpath', required=True, help='Official apksig 8.13.0 JAR')
parser.add_argument('--javac', default='javac')
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix='publisher-verifier-build-') as directory:
    subprocess.run([args.javac, '--release', '11', '-classpath', args.classpath, '-d', directory,
                    str(root / 'tools/ApkVerifierMain.java')], check=True)
    data = (Path(directory) / 'ApkVerifierMain.class').read_bytes()
    text = '// Generated from tools/ApkVerifierMain.java with javac --release 11.\n'
    text += 'export const VERIFIER_CLASS = ' + repr(base64.b64encode(data).decode()) + ';\n'
    text += 'export const VERIFIER_SHA256 = ' + repr(hashlib.sha256(data).hexdigest()) + ';\n'
    (root / 'src/verifier-bytecode.ts').write_text(text)
