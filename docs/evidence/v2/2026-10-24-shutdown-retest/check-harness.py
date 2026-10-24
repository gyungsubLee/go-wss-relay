"""Check profile parity and lifecycle gates without starting a process or opening a socket."""
import contextlib
import io
import json
from pathlib import Path
import runpy
import tempfile
from types import SimpleNamespace
from unittest.mock import patch


folder = Path(__file__).resolve().parent
config = json.loads((folder / 'run-config.json').read_text())
profiles = config['profiles']['go']
expected = [
    (200, 16, 20, 100, 15, 0),
    (100, 16, 10, 100, 10, 0),
    (200, 16, 10, 679, 30, 0),
    (200, 16, 10, 1127, 30, 0),
    (100, 8, 10, 679, 15, 500),
    (200, 16, 10, 679, 60, 0),
]
keys = ['participants', 'roomSize', 'publishHz', 'statePayloadBytes', 'measurementSeconds', 'slowReadMs']
assert [tuple(profile[key] for key in keys) for profile in profiles] == expected
assert sum(profile['priorPostMeasurementWriteErrors'] for profile in profiles) == 139
assert (folder / 'sdk-load.ts').read_bytes() == (folder.parent / '2026-10-24-auth-rate-retest/sdk-load.ts').read_bytes()
for source in folder.glob('*.py'):
    compile(source.read_text(), str(source), 'exec')


def run_fake_suite(tag, expected_passed, post_write_errors=0, legacy=False, shutdown_override=None, other_cases_pass=True):
    with tempfile.TemporaryDirectory(prefix='shutdown-harness-check-') as temporary:
        root = Path(temporary)
        evidence = root / 'docs/evidence/v2/retest'
        evidence.mkdir(parents=True)
        script = evidence / 'run-load.py'
        script.write_bytes((folder / 'run-load.py').read_bytes())
        binary_dir = root / config['binaryDirectory']
        binary_dir.mkdir(parents=True)
        for name in ('wss-relay', 'relay-load'):
            (binary_dir / name).write_bytes(b'check-only binary placeholder')
        credentials = root / '.cache/live-load-20261023'
        (credentials / 'tls').mkdir(parents=True)
        (credentials / 'operator.token').write_text('check-only-token')
        (credentials / 'tls/cert.pem').write_text('check-only-ca')
        traffic = config['default']['traffic']
        statuses = []
        case_reports = []
        cumulative_errors = 0
        for index, profile in enumerate(profiles):
            baseline = {
                'rooms': 0, 'connections': 0, 'reservedParticipants': 0,
                'rateRejected': 0, 'rateRejectedState': 0, 'queueOverflows': 0,
                'writeErrors': cumulative_errors, 'traffic': traffic,
                'ingressBytes': 100, 'egressBytes': 100,
            }
            before = dict(baseline, rooms=13, connections=profile['participants'], reservedParticipants=profile['participants'])
            after = dict(before, ingressBytes=200, egressBytes=200)
            cumulative_errors += post_write_errors if index == 0 else 0
            cleanup = dict(baseline, writeErrors=cumulative_errors)
            statuses.extend([baseline, cleanup])
            case_reports.append({
                **{key: profile[key] for key in keys},
                'connected': profile['participants'], 'receivedStates': 10000,
                'rollingSampleCount': 100, 'p95Ms': 40, 'p99Ms': 50,
                'peakRssKiB': 10000, 'peakCpuPercent': 1, 'failures': 0,
                'ingressBytesPerSecond': 100, 'egressBytesPerSecond': 100,
                'nodeBefore': before, 'nodeAfter': after,
            })
            if not legacy:
                case_reports[-1].update(
                    leftAcknowledgements=profile['participants'], normalClosures=profile['participants'],
                    shutdownFailures=0, shutdownForcedClosures=0, shutdownDurationMs=25,
                )
            if index == 0 and shutdown_override:
                for key, value in shutdown_override.items():
                    if value is None:
                        case_reports[-1].pop(key, None)
                    else:
                        case_reports[-1][key] = value
        requests = iter([{'maxParticipants': 200, 'maxRooms': 512}, *statuses])
        reports = iter(case_reports)
        wall_clock, monotonic_clock = [], []
        wall_now, monotonic_now = 1_000_000_000_000, 1_000_000
        for index in range(len(profiles)):
            wall_elapsed = 1000 + (3_600_000_000_000 if index == 0 else 0)
            wall_clock.extend([wall_now, wall_now + wall_elapsed])
            monotonic_clock.extend([monotonic_now, monotonic_now + 1000])
            wall_now += wall_elapsed + 1000
            monotonic_now += 2000

        def fake_request(*arguments, **keywords):
            return contextlib.closing(io.BytesIO(json.dumps(next(requests)).encode()))

        def fake_load(command, **keywords):
            output = Path(command[command.index('--out') + 1])
            output.write_text(json.dumps(next(reports)))
            return SimpleNamespace(returncode=0)

        command = f'{binary_dir / "wss-relay"} --listen 127.0.0.1:19443 --management-listen 127.0.0.1:19092'
        arguments = [str(script), '--suite', 'shutdown', '--tag', tag, '--server-pid', '12345',
                     '--continue-on-failure', '--failure-note', 'Synthetic post-measurement errors must fail']
        with patch('sys.argv', arguments), patch('subprocess.check_output', return_value=command), \
             patch('subprocess.run', side_effect=fake_load), patch('urllib.request.urlopen', side_effect=fake_request), \
             patch('time.sleep'), patch('time.time_ns', side_effect=wall_clock), \
             patch('time.monotonic_ns', side_effect=monotonic_clock), contextlib.redirect_stdout(io.StringIO()):
            try:
                runpy.run_path(str(script), run_name='__main__')
            except SystemExit as error:
                exit_code = error.code
            else:
                raise AssertionError('Runner did not report an exit code')
        summary = json.loads((evidence / f'shutdown-{tag}-summary.json').read_text())
        assert len(summary) == 6
        assert [tuple(row[key] for key in keys) for row in summary] == expected
        assert exit_code == (0 if expected_passed and other_cases_pass else 1), exit_code
        assert summary[0]['passed'] is expected_passed
        assert summary[0]['postMeasurementMetricDelta']['writeErrors'] == post_write_errors
        assert summary[0]['phaseMetricDeltas'][-1]['metricDelta']['writeErrors'] == post_write_errors
        assert summary[0]['cleanup']['writeErrors'] == post_write_errors
        timing = summary[0]['subprocessTiming']
        assert timing['elapsedWallNs'] == timing['finishedUnixNs'] - timing['startedUnixNs']
        assert timing['elapsedMonotonicNs'] == 1000
        assert timing['wallMinusMonotonicNs'] == 3_600_000_000_000
        assert summary[-1]['subprocessTiming']['wallMinusMonotonicNs'] == 0
        assert all(row['passed'] is other_cases_pass for row in summary[1:])
        assert all(row['legacyShutdownReport'] is (tag == 'baseline' and legacy) for row in summary)
        if not legacy:
            assert summary[-1]['leftAcknowledgements'] == profiles[-1]['participants']
            assert summary[-1]['normalClosures'] == profiles[-1]['participants']
            assert summary[-1]['shutdownDurationMs'] == 25
        files_before = {path.name: path.read_bytes() for path in evidence.iterdir()}
        with patch('sys.argv', arguments), patch('subprocess.check_output', return_value=command), \
             patch('urllib.request.urlopen', side_effect=lambda *a, **k: contextlib.closing(io.BytesIO(b'{"maxParticipants":200,"maxRooms":512}'))):
            try:
                runpy.run_path(str(script), run_name='__main__')
            except SystemExit as error:
                assert 'Evidence already exists' in str(error)
            else:
                raise AssertionError('Existing evidence was not rejected')
        assert {path.name: path.read_bytes() for path in evidence.iterdir()} == files_before


run_fake_suite('baseline', True, legacy=True)
run_fake_suite('final', False, legacy=True, other_cases_pass=False)
run_fake_suite('baseline-extra', False, legacy=True, other_cases_pass=False)
run_fake_suite('final', True)
run_fake_suite('final', False, post_write_errors=1)
run_fake_suite('baseline', False, legacy=True, post_write_errors=1)
for override in [
    {'leftAcknowledgements': 199}, {'normalClosures': 199}, {'shutdownFailures': 1},
    {'shutdownForcedClosures': 1}, {'shutdownDurationMs': 0}, {'shutdownDurationMs': None},
]:
    run_fake_suite('final', False, shutdown_override=override)
run_fake_suite('baseline', False, shutdown_override={'shutdownFailures': 1})
run_fake_suite('baseline', False, shutdown_override={'shutdownDurationMs': None})
print('harness checks passed: six profiles, strict lifecycle and shutdown gates, exact legacy baseline tag, clock discrepancy recording, overwrite refusal; no sockets or load processes')
