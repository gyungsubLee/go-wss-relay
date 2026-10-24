"""Retest the six profiles that previously recorded 139 post-measurement write errors."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import time
import urllib.parse
import urllib.request


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--suite', required=True, choices=['shutdown'])
parser.add_argument('--management', default='http://127.0.0.1:19092', help='local management origin')
parser.add_argument('--server-port', type=int, default=19443, help='public node port for exact PID lookup')
parser.add_argument('--tag', default='', help='fresh evidence label, for example baseline or final')
parser.add_argument('--server-pid', required=True, type=int, help='PID of the exact server started by this test operator')
parser.add_argument('--binary-dir', default='out/shutdown-retest-20261024', help='repository-relative freshly built binary directory')
parser.add_argument('--continue-on-failure', action='store_true', help='collect all six baseline cases; the suite still exits 1 on any failure')
parser.add_argument('--failure-note', default='', help='required explanation when continuing after a failed gate')
args = parser.parse_args()
if args.tag and not re.fullmatch(r'[A-Za-z0-9_-]+', args.tag):
    parser.error('--tag must contain only letters, numbers, hyphens, or underscores')
if args.continue_on_failure and not args.failure_note.strip():
    parser.error('--continue-on-failure requires an explicit --failure-note')
server_port = args.server_port
management = args.management
url = urllib.parse.urlsplit(management)
if url.scheme not in ('http', 'https') or url.hostname not in ('localhost', '127.0.0.1', '::1') or url.username or url.password or url.query or url.fragment or url.path not in ('', '/'):
    parser.error('--management must be a local HTTP(S) origin')
if not 1 <= server_port <= 65535:
    parser.error('--server-port must be between 1 and 65535')

folder = Path(__file__).resolve().parent
root = folder.parents[3]
token = (root / '.cache/live-load-20261023/operator.token').read_text().strip()
pid = args.server_pid
server_command = shlex.split(subprocess.check_output(['ps', '-p', str(pid), '-o', 'command='], text=True).strip())
expected_binary = (root / args.binary_dir / 'wss-relay').resolve()
if not server_command or (root / server_command[0]).resolve() != expected_binary:
    raise SystemExit('The supplied server PID does not run the declared test binary')
if '--listen' not in server_command or server_command[server_command.index('--listen') + 1] != f'127.0.0.1:{server_port}':
    raise SystemExit('The supplied server PID does not use the declared public port')
if '--management-listen' not in server_command or server_command[server_command.index('--management-listen') + 1] != f'127.0.0.1:{url.port or (443 if url.scheme == "https" else 80)}':
    raise SystemExit('The supplied server PID does not use the declared management port')


def sha256(path):
    with path.open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def node_request(endpoint):
    request = urllib.request.Request(management.rstrip('/') + endpoint, headers={'Authorization': 'Bearer ' + token})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def is_empty(stats):
    return all(stats[key] == 0 for key in ['rooms', 'connections', 'reservedParticipants'])


profiles = [
    (200, 16, 20, 100, 15, 0),
    (100, 16, 10, 100, 10, 0),
    (200, 16, 10, 679, 30, 0),
    (200, 16, 10, 1127, 30, 0),
    (100, 8, 10, 679, 15, 500),
    (200, 16, 10, 679, 60, 0),
]
expected_traffic = {
    'nodeIngressBytesPerSecond': 8388608,
    'nodeEgressBytesPerSecond': 67108864,
    'roomIngressBytesPerSecond': 524288,
    'roomEgressBytesPerSecond': 4194304,
}
summary_stem = case_prefix = args.suite
if args.tag:
    summary_stem += '-' + args.tag
    case_prefix += '-' + args.tag

inventory = node_request('/v2/node')
if inventory['maxParticipants'] != 200 or inventory['maxRooms'] != 512:
    raise SystemExit('Expected unchanged limits: 200 participant slots and 512 rooms')
summary_file = folder / (summary_stem + '-summary.json')
case_names = [f'{case_prefix}-c{participants}-n{room_size}-f{hz}-b{payload}-s{seconds}-slow{slow}'
              for participants, room_size, hz, payload, seconds, slow in profiles]
if summary_file.exists() or any((folder / (name + extension)).exists() for name in case_names for extension in ('.json', '.log')):
    raise SystemExit('Evidence already exists; use a fresh --tag')
provenance = {
    'serverPID': pid, 'serverCommand': server_command,
    'binarySha256': {name: sha256(root / args.binary_dir / name) for name in ('wss-relay', 'relay-load')},
    'testCASha256': sha256(root / '.cache/live-load-20261023/tls/cert.pem'),
}
summary = []
for participants, room_size, hz, payload, seconds, slow in profiles:
    baseline = node_request('/v2/status')
    if not is_empty(baseline):
        raise SystemExit('Baseline must have zero rooms, connections, and reserved participants')
    if baseline['traffic'] != expected_traffic:
        raise SystemExit('Traffic byte budgets must match the original defaults')
    name = f'{case_prefix}-c{participants}-n{room_size}-f{hz}-b{payload}-s{seconds}-slow{slow}'
    report_file = folder / (name + '.json')
    if report_file.exists() or (folder / (name + '.log')).exists():
        raise SystemExit(f'{name}: evidence already exists; use a fresh --tag')
    command = [
        str(root / args.binary_dir / 'relay-load'), '--management', management,
        '--token-file', '.cache/live-load-20261023/operator.token',
        '--ca-file', '.cache/live-load-20261023/tls/cert.pem',
        '--participants', str(participants), '--room-size', str(room_size), '--hz', str(hz),
        '--payload', str(payload), '--seconds', str(seconds), '--warmup', '2',
        '--server-pid', str(pid), '--slow-read-ms', str(slow), '--out', str(report_file),
    ]
    with (folder / (name + '.log')).open('x') as log:
        process_started_unix_ns = time.time_ns()
        process_started_monotonic_ns = time.monotonic_ns()
        result = subprocess.run(command, cwd=root, stdout=log, stderr=subprocess.STDOUT, timeout=seconds + 40)
        process_finished_monotonic_ns = time.monotonic_ns()
        process_finished_unix_ns = time.time_ns()
    if not report_file.exists():
        raise SystemExit(f'{name}: no report; exit={result.returncode}')
    report = json.loads(report_file.read_text())
    before, after = report['nodeBefore'], report['nodeAfter']
    metric_keys = sorted({'rateRejected', 'queueOverflows', 'writeErrors'} | {key for key in before if key.startswith('rateRejected')})
    metric_delta = {key: after[key] - before[key] for key in metric_keys}
    byte_delta = {key: after[key] - before[key] for key in ['ingressBytes', 'egressBytes']}
    latency_passed = 0 <= report['p95Ms'] <= 100 and 0 <= report['p99Ms'] <= 200
    shutdown_keys = ['leftAcknowledgements', 'normalClosures', 'shutdownFailures', 'shutdownForcedClosures', 'shutdownDurationMs']
    shutdown_metrics_present = all(key in report for key in shutdown_keys)
    legacy_shutdown_report = args.tag == 'baseline' and not any(key in report for key in shutdown_keys)
    shutdown_passed = legacy_shutdown_report or (
        shutdown_metrics_present
        and report['leftAcknowledgements'] == report['connected']
        and report['normalClosures'] == report['connected']
        and report['shutdownFailures'] == 0 and report['shutdownForcedClosures'] == 0
        and report['shutdownDurationMs'] > 0
    )
    passed = (
        result.returncode == 0 and report['failures'] == 0
        and report['connected'] == participants and before['connections'] == participants and after['connections'] == participants
        and after['reservedParticipants'] == participants
        and report['receivedStates'] > participants and report['rollingSampleCount'] > 0
        and 0 < report['peakRssKiB'] <= 512 * 1024
        and 0 < report['peakCpuPercent'] <= (os.cpu_count() or 1) * 70
        and all(delta == 0 for delta in metric_delta.values())
        and all(delta > 0 for delta in byte_delta.values())
        and latency_passed
        and shutdown_passed
    )
    time.sleep(1.25)
    cleanup = node_request('/v2/status')
    lifecycle_delta = {key: cleanup[key] - baseline[key] for key in metric_keys}
    passed = passed and is_empty(cleanup) and all(delta == 0 for delta in lifecycle_delta.values())
    row = {key: report[key] for key in [
        'participants', 'roomSize', 'publishHz', 'statePayloadBytes', 'measurementSeconds',
        'slowReadMs', 'connected', 'receivedStates', 'p95Ms', 'p99Ms',
        'egressBytesPerSecond', 'peakRssKiB', 'peakCpuPercent', 'failures',
    ]}
    row.update(
        name=name, passed=passed, exitCode=result.returncode,
        shutdownMetricsPresent=shutdown_metrics_present, legacyShutdownReport=legacy_shutdown_report,
        shutdownGateApplied=not legacy_shutdown_report, shutdownPassed=shutdown_passed,
        subprocessTiming={
            'startedUnixNs': process_started_unix_ns,
            'finishedUnixNs': process_finished_unix_ns,
            'elapsedWallNs': process_finished_unix_ns - process_started_unix_ns,
            'elapsedMonotonicNs': process_finished_monotonic_ns - process_started_monotonic_ns,
            'wallMinusMonotonicNs': (process_finished_unix_ns - process_started_unix_ns)
                - (process_finished_monotonic_ns - process_started_monotonic_ns),
        },
        latencyGateApplied=True, latencyWithinNormalGate=latency_passed,
        nodeLimits={key: inventory[key] for key in ['maxRooms', 'maxParticipants']}, traffic=before['traffic'],
        baseline=baseline, before=before, after=after, cleanup=cleanup,
        metricDelta=metric_delta,
        metricScope='Every error counter must stay unchanged across setup, warmup, measurement, and cleanup; no write errors are excluded',
        setupAndWarmupMetricDelta={key: before[key] - baseline[key] for key in metric_keys},
        postMeasurementMetricDelta={key: cleanup[key] - after[key] for key in metric_keys},
        lifecycleMetricDelta=lifecycle_delta,
        phaseMetricDeltas=[
            {'from': 'baseline', 'to': 'measurement-start', 'metricDelta': {key: before[key] - baseline[key] for key in metric_keys}},
            {'from': 'measurement-start', 'to': 'measurement-end', 'metricDelta': metric_delta},
            {'from': 'measurement-end', 'to': 'cleanup', 'metricDelta': {key: cleanup[key] - after[key] for key in metric_keys}},
        ],
        provenance=provenance, continuationNote=args.failure_note if args.continue_on_failure else None,
        protobufByteDelta=byte_delta,
        receivedStatePayloadBytes=report['receivedStates'] * payload,
        ingressBytesPerSecond=report['ingressBytesPerSecond'],
    )
    row.update({key: report[key] for key in shutdown_keys if key in report})
    summary.append(row)
    print(json.dumps(row), flush=True)
    if not passed and not args.continue_on_failure:
        break
with summary_file.open('x') as output:
    output.write(json.dumps(summary, indent=2) + '\n')
raise SystemExit(0 if len(summary) == len(profiles) and all(row['passed'] for row in summary) else 1)
