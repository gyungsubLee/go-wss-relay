#!/usr/bin/env python3
"""Run the declared tiers sequentially; never escalate after a failed gate."""
import argparse
import json
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--server-pid', required=True)
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent
folder = root / 'docs/evidence/v2/load'
profile = json.loads((folder / 'profile.json').read_text())
gates = profile['gates']
results = []
for participants in profile['participants']:
    for room_size in profile['roomSizes']:
        for hz in profile['publishHz']:
            output = folder / f'c{participants}-n{room_size}-f{hz}.json'
            subprocess.run([
                str(root / 'out/relay-load'), '--participants', str(participants),
                '--room-size', str(room_size), '--hz', str(hz),
                '--payload', str(profile['payloadBytes']),
                '--warmup', str(profile['warmupSeconds']),
                '--seconds', str(profile['measurementSeconds']),
                '--server-pid', args.server_pid, '--out', str(output),
            ], cwd=root, check=True, stdout=subprocess.DEVNULL)
            report = json.loads(output.read_text())
            passed = (report['failures'] == gates['unexpectedFailures']
                      and 0 <= report['p95Ms'] <= gates['p95Ms']
                      and 0 <= report['p99Ms'] <= gates['p99Ms']
                      and report['peakRssKiB'] <= gates['rssMiB'] * 1024
                      and report['peakCpuPercent'] <= profile['cpus'] * gates['cpuPercentOfAllLogicalCores'])
            row = {key: report[key] for key in ['participants', 'roomSize', 'publishHz', 'p95Ms', 'p99Ms', 'egressBytesPerSecond', 'peakRssKiB', 'peakCpuPercent', 'failures']}
            row['passed'] = passed
            results.append(row)
            (folder / 'matrix-summary.json').write_text(json.dumps(results, indent=2) + '\n')
            print(json.dumps(row), flush=True)
            if not passed:
                raise SystemExit('Gate failed; higher concurrency was not attempted.')
    print(f'Tier {participants} passed; proceeding to the next declared tier.', flush=True)
