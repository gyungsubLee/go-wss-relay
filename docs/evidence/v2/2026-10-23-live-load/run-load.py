"""Run one declared local WSS suite, stopping at the first failed gate."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.parse
import urllib.request


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--suite', required=True, choices=['initial', 'diagnose', 'normal', 'ten-hz', 'stress', 'saturation'])
parser.add_argument('--management', help='local management URL; default port 19092, or 19093 for stress')
parser.add_argument('--server-port', type=int, help='public node port for exact PID lookup; default 19443, or 19444 for stress')
parser.add_argument('--tag', default='', help='optional filename suffix, for example room4mib or room8mib')
args = parser.parse_args()
if args.tag and not re.fullmatch(r'[A-Za-z0-9_-]+', args.tag):
    parser.error('--tag must contain only letters, numbers, hyphens, or underscores')
server_port = args.server_port if args.server_port is not None else (19444 if args.suite == 'stress' else 19443)
management = args.management or f'http://127.0.0.1:{19093 if args.suite == "stress" else 19092}'
url = urllib.parse.urlsplit(management)
if url.scheme not in ('http', 'https') or url.hostname not in ('localhost', '127.0.0.1', '::1') or url.username or url.password or url.query or url.fragment or url.path not in ('', '/'):
    parser.error('--management must be a local HTTP(S) origin')
if not 1 <= server_port <= 65535:
    parser.error('--server-port must be between 1 and 65535')

folder = Path(__file__).resolve().parent
root = folder.parents[3]
token = (root / '.cache/live-load-20261023/operator.token').read_text().strip()
pattern = rf'^out/live-load-20261023/wss-relay --listen 127\.0\.0\.1:{server_port}( |$)'
pids = subprocess.check_output(['pgrep', '-f', pattern], text=True).split()
if len(pids) != 1:
    raise SystemExit(f'Expected one node on port {server_port}; found {len(pids)}')
pid = int(pids[0])


def node_request(endpoint):
    request = urllib.request.Request(management.rstrip('/') + endpoint, headers={'Authorization': 'Bearer ' + token})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def is_empty(stats):
    return all(stats[key] == 0 for key in ['rooms', 'connections', 'reservedParticipants'])


normal = [
    (200, 8, 20, 100, 15, 0),
    (200, 16, 20, 100, 15, 0),
    (200, 16, 20, 679, 30, 0),
    (200, 16, 10, 1127, 30, 0),
    (100, 8, 20, 679, 20, 500),
    (200, 16, 20, 679, 60, 0),
]
profiles = {
    'initial': [(100, 2, 20, 100, 10, 0)] + normal,
    'diagnose': [(2, 2, 20, 100, 10, 0), (100, 2, 10, 100, 10, 0), (100, 2, 20, 100, 15, 0)],
    'normal': normal,
    'ten-hz': [(100, 16, 10, 100, 10, 0), (200, 16, 10, 679, 30, 0), (200, 16, 10, 1127, 30, 0), (100, 8, 10, 679, 15, 500), (200, 16, 10, 679, 60, 0)],
    'stress': [(500, 8, 10, 100, 10, 0), (1000, 8, 10, 100, 10, 0), (1000, 16, 10, 100, 10, 0)],
    'saturation': [(16, 16, 18, 1127, 15, 0)],
}
summary_stem = {'initial': 'default', 'diagnose': 'diagnostic'}.get(args.suite, args.suite)
case_prefix = args.suite if args.suite in ('stress', 'saturation') else 'default'
if args.tag:
    summary_stem += '-' + args.tag
    case_prefix += '-' + args.tag

inventory = node_request('/v2/node')
expected_cap = 1024 if args.suite == 'stress' else 200
if inventory['maxParticipants'] != expected_cap:
    raise SystemExit(f'Expected {expected_cap} participant slots; node reports {inventory["maxParticipants"]}')
summary = []
for participants, room_size, hz, payload, seconds, slow in profiles[args.suite]:
    baseline = node_request('/v2/status')
    if not is_empty(baseline):
        raise SystemExit('Baseline must have zero rooms, connections, and reserved participants')
    name = f'{case_prefix}-c{participants}-n{room_size}-f{hz}-b{payload}-s{seconds}-slow{slow}'
    report_file = folder / (name + '.json')
    command = [
        str(root / 'out/live-load-20261023/relay-load'), '--management', management,
        '--token-file', '.cache/live-load-20261023/operator.token',
        '--ca-file', '.cache/live-load-20261023/tls/cert.pem',
        '--participants', str(participants), '--room-size', str(room_size), '--hz', str(hz),
        '--payload', str(payload), '--seconds', str(seconds), '--warmup', '2',
        '--server-pid', str(pid), '--slow-read-ms', str(slow), '--out', str(report_file),
    ]
    with (folder / (name + '.log')).open('w') as log:
        result = subprocess.run(command, cwd=root, stdout=log, stderr=subprocess.STDOUT, timeout=seconds + 40)
    if not report_file.exists():
        raise SystemExit(f'{name}: no report; exit={result.returncode}')
    report = json.loads(report_file.read_text())
    before, after = report['nodeBefore'], report['nodeAfter']
    metric_delta = {key: after[key] - before[key] for key in ['rateRejected', 'queueOverflows', 'writeErrors']}
    byte_delta = {key: after[key] - before[key] for key in ['ingressBytes', 'egressBytes']}
    latency_passed = 0 <= report['p95Ms'] <= 100 and 0 <= report['p99Ms'] <= 200
    passed = (
        result.returncode == 0 and report['failures'] == 0
        and report['connected'] == participants and before['connections'] == participants and after['connections'] == participants
        and after['reservedParticipants'] == participants
        and report['receivedStates'] > participants and report['rollingSampleCount'] > 0
        and 0 < report['peakRssKiB'] <= 512 * 1024
        and 0 < report['peakCpuPercent'] <= (os.cpu_count() or 1) * 70
        and all(delta == 0 for delta in metric_delta.values())
        and all(delta > 0 for delta in byte_delta.values())
        and (latency_passed or args.suite == 'saturation')
    )
    time.sleep(1.25)
    cleanup = node_request('/v2/status')
    passed = passed and is_empty(cleanup)
    row = {key: report[key] for key in [
        'participants', 'roomSize', 'publishHz', 'statePayloadBytes', 'measurementSeconds',
        'slowReadMs', 'connected', 'receivedStates', 'p95Ms', 'p99Ms',
        'egressBytesPerSecond', 'peakRssKiB', 'peakCpuPercent', 'failures',
    ]}
    row.update(
        name=name, passed=passed, exitCode=result.returncode,
        latencyGateApplied=args.suite != 'saturation', latencyWithinNormalGate=latency_passed,
        nodeLimits={key: inventory[key] for key in ['maxRooms', 'maxParticipants']}, traffic=before['traffic'],
        baseline={key: baseline[key] for key in ['rooms', 'connections', 'reservedParticipants']},
        cleanup={key: cleanup[key] for key in ['rooms', 'connections', 'reservedParticipants', 'goroutines', 'heapAllocBytes']},
        metricDelta=metric_delta, protobufByteDelta=byte_delta,
        receivedStatePayloadBytes=report['receivedStates'] * payload,
        ingressBytesPerSecond=report['ingressBytesPerSecond'],
    )
    summary.append(row)
    (folder / (summary_stem + '-summary.json')).write_text(json.dumps(summary, indent=2) + '\n')
    print(json.dumps(row), flush=True)
    if not passed:
        raise SystemExit('Gate failed; no escalation')
