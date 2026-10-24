#!/usr/bin/env python3
"""Run: python3 infrastructure/scripts/tests/traffic-budget.py (stdlib only, no host mutations)."""
import sys
sys.dont_write_bytecode = True
import importlib.util
import contextlib
import io
import tempfile
import os
import subprocess
from pathlib import Path

spec = importlib.util.spec_from_file_location("traffic", Path(__file__).parents[1] / "traffic_budget.py")
traffic = importlib.util.module_from_spec(spec)
spec.loader.exec_module(traffic)

for instance in ('standalone', 'coordinator', 'relay', 'invalid'):
    os.environ['WSS_INSTANCE'] = instance
    calls = []
    original_run = traffic.subprocess.run
    traffic.subprocess.run = lambda command, **kwargs: calls.append(command)
    try: traffic.stop_public_services()
    finally: traffic.subprocess.run = original_run
    instances = ('standalone', 'coordinator', 'relay') if instance == 'invalid' else (instance,)
    units = [f'wss-relay@{name}.service' for name in instances] + ['caddy.service']
    assert calls == [['systemctl', 'stop', *units], ['systemctl', 'mask', '--runtime', *units]]


# A valid but wrong env role must not leave the installed instance running.
with tempfile.TemporaryDirectory() as directory:
    selection = Path(directory) / 'instance'
    selection.write_text('standalone\n')
    previous_file = getattr(traffic, 'INSTANCE_FILE', None)
    traffic.INSTANCE_FILE = selection
    os.environ['WSS_INSTANCE'] = 'coordinator'
    calls = []
    original_run = traffic.subprocess.run
    traffic.subprocess.run = lambda command, **kwargs: calls.append(command)
    try:
        traffic.stop_public_services()
    finally:
        traffic.subprocess.run = original_run
        if previous_file is not None:
            traffic.INSTANCE_FILE = previous_file
    units = [f'wss-relay@{name}.service' for name in ('standalone', 'coordinator', 'relay')] + ['caddy.service']
    assert calls == [['systemctl', 'stop', *units], ['systemctl', 'mask', '--runtime', *units]], 'role mismatch left installed service running'

assert "EnvironmentFile=-/etc/wss-relay/traffic.env" in (Path(__file__).parents[2] / "ops/traffic-guard.service").read_text(), "missing env must still run guard to stop active services"

with tempfile.TemporaryDirectory() as d:
    root = Path(d)
    calls = []
    guard = traffic.Guard(root, "eth0", (100, 300), lambda: calls.append("stop"))
    guard.sample("2026-09", "boot1", 20, 30, initialize=True)
    assert traffic.load(root / "traffic.json")["total_bytes"] == 50
    warning = io.StringIO()
    with contextlib.redirect_stdout(warning):
        guard.sample("2026-09", "boot1", 50, 50)
    assert warning.getvalue() == 'WARNING traffic total_bytes=100 rx_bytes=50 tx_bytes=50\n'
    assert traffic.load(root / "traffic.json")["total_bytes"] == 100 and calls == []
    warning = io.StringIO()
    with contextlib.redirect_stdout(warning):
        guard.sample("2026-09", "boot1", 150, 150)
    assert warning.getvalue() == 'WARNING traffic total_bytes=300 rx_bytes=150 tx_bytes=150\n'
    assert (root / "traffic-stopped").exists() and calls == ["stop"]
    calls.clear()
    guard.sample("2026-10", "boot1", 160, 155)
    assert traffic.load(root / "traffic.json")["total_bytes"] == 15
    assert calls == ["stop"], "a new month must not clear the stop latch"

for failure in ("missing", "corrupt", "reboot", "reset", "interface", "backward"):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        calls = []
        guard = traffic.Guard(root, "eth0", (100, 300), lambda: calls.append("stop"))
        if failure != "missing":
            guard.sample("2026-09", "boot1", 10, 10, initialize=True)
        if failure == "corrupt":
            (root / "traffic.json").write_text("broken")
        if failure == "interface":
            guard.interface = "eth1"
        try:
            guard.sample("2026-08" if failure == "backward" else "2026-09", "boot2" if failure == "reboot" else "boot1", 1 if failure == "reset" else 20, 20)
        except traffic.Uncertain:
            pass
        else:
            raise AssertionError(f"{failure} must report uncertainty")
        assert (root / "traffic-stopped").exists() and "stop" in calls

for limits in [(0, 1), (2, 1), (1, 3_000_000_000_000)]:
    try:
        traffic.validate_limits(limits)
    except ValueError:
        pass
    else:
        raise AssertionError("unsafe thresholds accepted")
# A failed disk write must still stop public services even if no marker can persist.
with tempfile.TemporaryDirectory() as d:
    calls = []
    guard = traffic.Guard(Path(d) / "missing", "eth0", (100, 300), lambda: calls.append("stop"))
    try:
        guard.sample("2026-09", "boot", 1, 1)
    except OSError:
        pass
    assert calls == ["stop"]
# Execute startup mode in a fake VM filesystem. Only the root-UID requirement is
# omitted in this copied test script; counter, marker and CLI paths stay real.
for failure in (None, "reboot", "corrupt", "stopped", "over", "missing-counter", "initialize-first", "initialize-existing", "legacy", "invalid-instance", "instance-mismatch"):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        state = root / "state"; state.mkdir()
        counters = root / "net/eth0/statistics"; counters.mkdir(parents=True)
        (counters / "rx_bytes").write_text("20")
        (counters / "tx_bytes").write_text("20")
        (root / "boot_id").write_text("boot2" if failure == "reboot" else "boot1")
        month = traffic.datetime.datetime.now(traffic.datetime.timezone.utc).strftime("%Y-%m")
        if failure not in ('initialize-first', 'legacy'):
            traffic.Guard(state, "eth0", (100, 300)).sample(month, "boot1", 10, 10, initialize=True)
        if failure == 'legacy':
            (root / 'legacy').mkdir()
            (root / 'legacy/traffic.json').write_text('unreconciled legacy ledger')
        if failure == 'missing-counter':
            (counters / 'rx_bytes').unlink()
        if failure == "corrupt": (state / "traffic.json").write_text("bad")
        if failure == "stopped": (state / "traffic-stopped").touch()
        if failure == "over": (counters / "rx_bytes").write_text("350")
        script = Path(spec.origin).read_text()
        for original, replacement in [("/etc/wss-relay/instance", str(root / "instance")), ("/var/lib/wss-relay", str(state)), ("/var/lib/lobby-relay", str(root / "legacy")), ("/sys/class/net", str(root / "net")), ("/proc/sys/kernel/random/boot_id", str(root / "boot_id"))]:
            script = script.replace(original, replacement)
        script = script.replace("os.geteuid() != 0 or info.st_uid != 0", "False")
        copied = root / "traffic.py"; copied.write_text(script)
        binary = root / "systemctl"
        binary.write_text('#!/bin/sh\necho called >> "'+str(root / 'systemctl-calls')+'"\nexit 1\n'); binary.chmod(0o755)
        env = {**os.environ, "PATH": str(root)+os.pathsep+os.environ["PATH"], "WSS_INSTANCE":"relay", "TRAFFIC_INTERFACE":"eth0", "TRAFFIC_WARN_BYTES":"100", "TRAFFIC_STOP_BYTES":"300"}
        if failure == 'invalid-instance':
            env.pop('WSS_INSTANCE')
        if failure == 'instance-mismatch':
            (root / 'instance').write_text('coordinator\n')
        mode = '--initialize' if failure in ('initialize-first', 'initialize-existing', 'legacy') else '--check-start'
        arguments = [] if failure in ('invalid-instance', 'instance-mismatch') else [mode]
        result = subprocess.run([sys.executable, str(copied), *arguments], env=env, capture_output=True, text=True, timeout=10)
        assert result.returncode == (0 if failure in (None, "initialize-first") else 1), (failure, result.stdout, result.stderr)
        if (mode == '--check-start' and failure not in ('invalid-instance', 'instance-mismatch')) or failure == 'initialize-first':
            assert not (root / "systemctl-calls").exists(), "startup preflight must never stop its own systemd job"
        if failure not in (None, 'initialize-first'):
            assert (state / "traffic-stopped").exists()
        if failure in ('invalid-instance', 'instance-mismatch'):
            assert (root / 'systemctl-calls').exists()
        if failure == 'legacy':
            assert not (state / 'traffic.json').exists()
            assert (root / 'legacy/traffic.json').read_text() == 'unreconciled legacy ledger'
        if failure == 'initialize-existing':
            assert traffic.load(state / 'traffic.json')['total_bytes'] == 20
print("traffic budget: accounting, persistent stop and cold-start gate assertions passed")

for existing in ('traffic.json', 'traffic-stopped'):
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / existing).write_text('existing bytes')
        calls = []
        guard = traffic.Guard(root, 'eth0', (100, 300), lambda: calls.append('stop'))
        try: guard.sample('2026-10', 'boot', 1, 1, initialize=True)
        except traffic.Uncertain: pass
        else: raise AssertionError('initialization reset existing state')
        if existing == 'traffic.json': assert (root / existing).read_text() == 'existing bytes'
        assert calls and (root / 'traffic-stopped').exists()
print('explicit initialization never resets existing ledger or marker')
