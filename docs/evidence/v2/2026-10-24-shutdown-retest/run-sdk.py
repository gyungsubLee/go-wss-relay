"""Run actual SDK clients and sample the separate local node process."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import time
import urllib.request


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--hz', type=int, choices=[10, 20], default=20)
parser.add_argument('--tag', default='', help='fresh evidence label, for example baseline or final')
parser.add_argument('--server-pid', required=True, type=int, help='PID of the server started by this test operator')
parser.add_argument('--binary-dir', default='out/shutdown-retest-20261024')
args = parser.parse_args()
if args.tag and not re.fullmatch(r'[A-Za-z0-9_-]+', args.tag):
    parser.error('--tag must contain only letters, numbers, hyphens, or underscores')
folder = Path(__file__).resolve().parent
root = folder.parents[3]
token = (root / '.cache/live-load-20261023/operator.token').read_text().strip()
pid = args.server_pid
server_command = shlex.split(subprocess.check_output(['ps', '-p', str(pid), '-o', 'command='], text=True).strip())
if not server_command or (root / server_command[0]).resolve() != (root / args.binary_dir / 'wss-relay').resolve():
    raise SystemExit('The supplied server PID does not run the declared test binary')
if '--listen' not in server_command or server_command[server_command.index('--listen') + 1] != '127.0.0.1:19443':
    raise SystemExit('The supplied server PID does not use public port 19443')
if '--management-listen' not in server_command or server_command[server_command.index('--management-listen') + 1] != '127.0.0.1:19092':
    raise SystemExit('The supplied server PID does not use management port 19092')


def stats(endpoint='/v2/status'):
    request = urllib.request.Request('http://127.0.0.1:19092' + endpoint, headers={'Authorization': 'Bearer ' + token})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


inventory = stats('/v2/node')
if inventory['maxParticipants'] != 200 or inventory['maxRooms'] != 512:
    raise SystemExit('Expected unchanged limits: 200 participant slots and 512 rooms')
before = stats()
if any(before[key] != 0 for key in ['rooms', 'connections', 'reservedParticipants']):
    raise SystemExit('Baseline must have zero rooms, connections, and reserved participants')
if before['traffic'] != {
    'nodeIngressBytesPerSecond': 8388608, 'nodeEgressBytesPerSecond': 67108864,
    'roomIngressBytesPerSecond': 524288, 'roomEgressBytesPerSecond': 4194304,
}:
    raise SystemExit('Traffic byte budgets must match the original defaults')
suffix = ('-' + args.tag if args.tag else '') + f'-{args.hz}hz'
report_file = folder / ('sdk-load' + suffix + '.json')
if any((folder / (stem + suffix + extension)).exists() for stem, extension in [('sdk-load', '.json'), ('sdk-load', '.log'), ('sdk-process', '.json')]):
    raise SystemExit('Evidence already exists; use a fresh --tag')
environment = dict(os.environ, NODE_EXTRA_CA_CERTS=str(root / '.cache/live-load-20261023/tls/cert.pem'))
command = [
    'node', '--disable-warning=ExperimentalWarning', '--experimental-transform-types',
    str(folder / 'sdk-load.ts'), 'https://localhost:19443', '200', '16', '679', str(args.hz), '30', str(report_file),
]
samples, sampling_errors = [], []
started = time.monotonic()
with (folder / ('sdk-load' + suffix + '.log')).open('x') as log:
    process = subprocess.Popen(command, cwd=root, env=environment, stdout=log, stderr=subprocess.STDOUT)
    try:
        while process.poll() is None:
            raw = subprocess.check_output(['ps', '-p', str(pid), '-o', 'rss=', '-o', '%cpu='], text=True).split()
            if len(raw) != 2:
                raise RuntimeError('Invalid OS CPU/RSS sample')
            samples.append({
                'elapsedSeconds': time.monotonic() - started,
                'rssKiB': float(raw[0]), 'cpuPercent': float(raw[1]), 'stats': stats(),
            })
            if time.monotonic() - started > 160:
                raise TimeoutError('SDK test timeout')
            time.sleep(1)
    except Exception as error:
        sampling_errors.append(str(error))
    finally:
        if process.poll() is None:
            process.kill()
        code = process.wait()
after = stats()
time.sleep(1.25)
cleanup = stats()
sdk_report = json.loads(report_file.read_text()) if report_file.exists() else {}
peak_rss = max((sample['rssKiB'] for sample in samples), default=0)
peak_cpu = max((sample['cpuPercent'] for sample in samples), default=0)
metric_keys = sorted({'rateRejected', 'queueOverflows', 'writeErrors'} | {key for key in before if key.startswith('rateRejected')})
metric_delta = {key: after[key] - before[key] for key in metric_keys}
lifecycle_delta = {key: cleanup[key] - before[key] for key in metric_keys}
phase_deltas = []
snapshots = sdk_report.get('metricSnapshots', [])
for previous, current in zip(snapshots, snapshots[1:]):
    phase_deltas.append({
        'from': previous['label'], 'to': current['label'],
        'startedAtMs': previous['elapsedMs'], 'finishedAtMs': current['elapsedMs'],
        'metricDelta': {key: current['stats'][key] - previous['stats'][key] for key in metric_keys},
    })
passed = (
    code == 0 and sdk_report.get('passed') is True and not sampling_errors
    and bool(samples) and all(sample['rssKiB'] > 0 and sample['cpuPercent'] >= 0 for sample in samples)
    and 0 < peak_rss <= 512 * 1024 and 0 < peak_cpu <= (os.cpu_count() or 1) * 70
    and all(cleanup[key] == 0 for key in ['rooms', 'connections', 'reservedParticipants'])
    and all(delta == 0 for delta in metric_delta.values())
    and all(delta == 0 for delta in lifecycle_delta.values())
)
binary_hashes = {}
for name in ('wss-relay', 'relay-load'):
    with (root / args.binary_dir / name).open('rb') as binary:
        binary_hashes[name] = hashlib.file_digest(binary, 'sha256').hexdigest()
with (root / '.cache/live-load-20261023/tls/cert.pem').open('rb') as certificate:
    ca_hash = hashlib.file_digest(certificate, 'sha256').hexdigest()
process_report = {
    'command': command, 'passed': bool(passed), 'exitCode': code,
    'before': before, 'after': after, 'cleanup': cleanup, 'samples': samples, 'samplingErrors': sampling_errors,
    'peakRssKiB': peak_rss, 'peakCpuPercent': peak_cpu, 'metricDelta': metric_delta,
    'phaseMetricDeltas': phase_deltas,
    'postProcessMetricDelta': {key: cleanup[key] - after[key] for key in metric_keys},
    'lifecycleMetricDelta': lifecycle_delta,
    'metricScope': 'Every error counter must stay unchanged across the SDK lifecycle and cleanup; no write errors are excluded',
    'provenance': {'serverPID': pid, 'serverCommand': server_command, 'binarySha256': binary_hashes, 'testCASha256': ca_hash},
}
with (folder / ('sdk-process' + suffix + '.json')).open('x') as output:
    output.write(json.dumps(process_report, indent=2) + '\n')
print(json.dumps({'passed': bool(passed), 'exitCode': code, 'samples': len(samples), 'samplingErrors': sampling_errors}))
raise SystemExit(0 if passed else 1)
