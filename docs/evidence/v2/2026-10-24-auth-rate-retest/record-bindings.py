"""Record the source and binary hashes for one declared local retest phase."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--tag', required=True, help='fresh phase label, for example baseline or final')
parser.add_argument('--binary-dir', default='out/auth-rate-retest-20261024')
parser.add_argument('--extra-source', action='append', default=[], help='extra diagnostic overlay or source path to hash')
args = parser.parse_args()
if not re.fullmatch(r'[A-Za-z0-9_-]+', args.tag):
    parser.error('--tag must contain only letters, numbers, hyphens, or underscores')
folder = Path(__file__).resolve().parent
root = folder.parents[3]
output = folder / f'bindings-{args.tag}.json'
if output.exists():
    raise SystemExit('Bindings already exist; use a fresh --tag')


def sha256(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def git(*arguments):
    return subprocess.check_output(['git', *arguments], cwd=root, text=True).strip()


source_paths = git(
    'ls-files', '--cached', '--others', '--exclude-standard', '--',
    'internal', 'cmd', 'gen/go/relay/v2', 'client/src/relay', 'api/relay/v2', 'go.mod', 'go.sum',
).splitlines()
source_paths += [str(path.relative_to(root)) for path in folder.iterdir() if path.suffix in ('.py', '.ts') or path.name == 'run-config.json']
source_paths += ['client/package.json', 'client/package-lock.json', 'client/tsconfig.json']
source_paths += args.extra_source
for name in args.extra_source:
    if not (root / name).is_file():
        raise SystemExit(f'Extra diagnostic source does not exist: {name}')
sources = {name: sha256(root / name) for name in sorted(set(source_paths)) if (root / name).is_file()}
binary_paths = [root / args.binary_dir / name for name in ('wss-relay', 'relay-load')]
binaries = {str(path.relative_to(root)): sha256(path) for path in binary_paths}
build_info = {
    str(path.relative_to(root)): subprocess.check_output([str(root / '.tools/go/bin/go'), 'version', '-m', str(path)], text=True)
    for path in binary_paths
}
report = {
    'recordedAt': datetime.now(timezone.utc).isoformat(), 'tag': args.tag,
    'commit': git('rev-parse', 'HEAD'), 'gitStatus': git('status', '--short'),
    'os': platform.platform(), 'machine': platform.machine(),
    'logicalCPUs': os.cpu_count(), 'python': sys.version,
    'node': subprocess.check_output(['node', '--version'], text=True).strip(),
    'go': subprocess.check_output([str(root / '.tools/go/bin/go'), 'version'], text=True).strip(),
    'testCASha256': sha256(root / '.cache/live-load-20261023/tls/cert.pem'),
    'sourceSha256': sources, 'binarySha256': binaries, 'binaryBuildInfo': build_info,
    'extraSourcePaths': args.extra_source,
    'note': 'Hashes record files at capture time. Diagnostic overlay sources must be supplied explicitly; live results are separate artifacts.',
}
output.write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps({'output': str(output.relative_to(root)), 'sourceFiles': len(sources), 'binaries': list(binaries)}))
