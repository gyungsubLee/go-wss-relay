#!/usr/bin/env python3
"""Real deployment script, isolated filesystem and stubbed host commands."""
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

INFRA = Path(__file__).resolve().parents[2]
SHA = 'a' * 40
ROLES = ('standalone', 'coordinator', 'relay')


def executable(path, body):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body)
    path.chmod(0o755)


def scenario(role, failure=None, first=False):
    with tempfile.TemporaryDirectory(prefix='wss-deploy-') as tmp:
        root = Path(tmp).resolve()
        opt = root / 'opt/wss-relay'
        state = root / 'var/lib/wss-relay'
        etc = root / 'etc/wss-relay'
        for path in (opt / 'releases', state, etc / 'secrets', root / 'var/lock',
                     root / 'etc/systemd/system/caddy.service.d', root / 'etc/caddy', root / 'bin'):
            path.mkdir(parents=True, exist_ok=True)
        args = f'--role {role} --operator-token-file {etc}/secrets/operator.token --trusted-proxy 127.0.0.1/32'
        if role != 'coordinator':
            args += ' --public-url wss://play.example.com/v2/relay'
        else:
            args += f' --nodes-config {etc}/nodes.json'
        args += f' --traffic-config {etc}/traffic.json'
        (etc / f'{role}.env').write_text('RELAY_ARGS=' + args + '\n')
        (etc / 'secrets/operator.token').write_text('AQ' + 'A' * 41)
        (etc / 'secrets/operator.token').chmod(0o600)
        (etc / 'traffic.json').write_text('{}')
        (etc / 'nodes.json').write_text(json.dumps({'nodes': [{
            'id': 'node-a', 'controlUrl': 'https://control.example.com',
            'websocketUrl': 'wss://node.example.com/v2/relay',
            'tokenFile': str(etc / 'secrets/operator.token'),
        }]}))
        (etc / 'control-root.pem').write_text('live CA bytes, never overwrite')
        (etc / 'caddy.env').write_text(
            'STANDALONE_DOMAIN=play.example.com\nCOORDINATOR_DOMAIN=play.example.com\n'
            'RELAY_DOMAIN=play.example.com\nNODE_CONTROL_DOMAIN=control.example.com\n'
            'NODE_PRIVATE_IP=10.0.0.2\nCOORDINATOR_PRIVATE_IP=10.0.0.1\n')
        (etc / 'traffic.env').write_text(f'WSS_INSTANCE={role}\nTRAFFIC_INTERFACE=eth0\nTRAFFIC_WARN_BYTES=1000000\nTRAFFIC_STOP_BYTES=2000000\n')
        (state / 'traffic.json').write_text('live durable NIC accounting\n')
        if failure == 'marker':
            (state / 'traffic-stopped').touch()
        if failure == 'missing-ledger':
            (state / 'traffic.json').unlink()
        modifications = {
            'injection': 'RELAY_ARGS=$(touch ' + str(root / 'INJECTED') + ')\n',
            'unknown': 'RELAY_ARGS=' + args + ' --unknown value\n',
            'duplicate': 'RELAY_ARGS=' + args + ' --role relay\n',
            'listen': 'RELAY_ARGS=' + args + ' --listen 0.0.0.0:8090\n',
            'tls': 'RELAY_ARGS=' + args + ' --tls-cert /a --tls-key /b\n',
            'role': 'RELAY_ARGS=' + args.replace('--role ' + role, '--role invalid') + '\n',
            'relative-file': 'RELAY_ARGS=' + args.replace(str(etc) + '/secrets/operator.token', 'relative.token') + '\n',
            'capacity': 'RELAY_ARGS=' + args + ' --max-rooms 99999\n',
        }
        if failure in modifications:
            (etc / f'{role}.env').write_text(modifications[failure])
        if failure == 'token-mode':
            (etc / 'secrets/operator.token').chmod(0o644)
        if failure == 'token-cr':
            with (etc / 'secrets/operator.token').open('a') as f:
                f.write('\r')
        if failure == 'traffic':
            (etc / 'traffic.json').write_text('{"nodeIngressBytesPerSecond":1}')
        if failure == 'nodes':
            (etc / 'nodes.json').write_text('{"nodes":[]}')
        if failure == 'invalid-ca':
            config = json.loads((etc / 'nodes.json').read_text())
            config['nodes'][0]['caFile'] = str(etc / 'control-root.pem')
            (etc / 'nodes.json').write_text(json.dumps(config))
        if failure == 'caddy-env':
            (etc / 'caddy.env').write_text('RELAY_DOMAIN=$(bad)\n')
        if failure == 'reserved-ip':
            (etc / 'caddy.env').write_text((etc / 'caddy.env').read_text().replace('10.0.0.2', '240.0.0.1'))
        if failure == 'second-instance':
            (etc / 'instance').write_text('other\n')
        live = {p: p.read_bytes() for parent in (etc, state) for p in parent.rglob('*') if p.is_file()}

        def release(path, new):
            path.mkdir(parents=True)
            executable(path / 'wss-relay', '#!/bin/sh\nexit 0\n')
            shutil.copytree(INFRA / 'ops', path / 'ops')
            for asset in (path / 'ops').iterdir():
                asset.write_text(asset.read_text().replace('/opt/wss-relay', str(opt)).replace('/etc/wss-relay', str(etc)).replace('/var/lib/wss-relay', str(state)))
            (path / 'scripts').mkdir()
            for name in ('check-deployment.sh', 'check-traffic-budget.sh', 'traffic_budget.py', 'deploy-preflight.py', 'install-operator-token.py'):
                source = INFRA / 'scripts' / name
                if source.exists():
                    shutil.copy2(source, path / 'scripts' / name)
            # The checks themselves are tested separately. This fixture controls health/guard failure.
            executable(path / 'scripts/check-deployment.sh', '#!/bin/sh\nexit ' + ('1' if new and failure == 'health' else '0') + '\n')
            executable(path / 'scripts/check-traffic-budget.sh', '#!/bin/sh\nexit 0\n')
            files = sorted(p for p in path.rglob('*') if p.is_file())
            (path / 'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.relative_to(path).as_posix() + '\n' for p in files))

        old = opt / 'releases' / ('b' * 40)
        if not first:
            release(old, False)
            (opt / 'current').symlink_to(old)
            (etc / 'instance').write_text(role + '\n') if failure != 'second-instance' else None
        owned = [root / 'etc/caddy/Caddyfile', root / 'etc/systemd/system/wss-relay@.service',
                 root / 'etc/systemd/system/traffic-guard.service', root / 'etc/systemd/system/traffic-guard.timer',
                 root / 'etc/systemd/system/caddy.service.d/wss-relay.conf',
                 opt / 'ops/check-traffic-budget.sh', opt / 'ops/traffic_budget.py']
        if not first:
            for path in owned:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('previous ' + path.name + '\n')
        previous = {p: p.read_bytes() for p in owned if p.exists()}
        bundle = root / 'bundle'
        release(bundle, True)
        if failure == 'unlisted':
            (bundle / 'unverified').write_text('unlisted')
        if failure == 'checksum':
            (bundle / 'wss-relay').write_text('tampered')
        if failure == 'same-sha':
            shutil.copytree(bundle, opt / 'releases' / SHA)
            (opt / 'releases' / SHA / 'wss-relay').write_text('different bytes')
        archive = root / 'release.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            for path in bundle.rglob('*'):
                tar.add(path, arcname=str(path.relative_to(bundle)), recursive=False)
            if failure in ('traversal', 'absolute', 'symlink', 'hardlink', 'device', 'special', 'count', 'size'):
                info = tarfile.TarInfo('../escape' if failure == 'traversal' else '/escape' if failure == 'absolute' else 'evil')
                if failure == 'symlink': info.type = tarfile.SYMTYPE; info.linkname = '/tmp/escape'
                if failure == 'hardlink': info.type = tarfile.LNKTYPE; info.linkname = 'wss-relay'
                if failure == 'device': info.type = tarfile.CHRTYPE
                if failure == 'special': info.mode = 0o4755
                if failure == 'size':
                    info.size = 151 * 1024 * 1024
                    tar.addfile(info, io.BytesIO(bytes(info.size)))
                elif failure == 'count':
                    for n in range(31):
                        info = tarfile.TarInfo(f'extra{n}'); tar.addfile(info)
                else: tar.addfile(info)
        executable(root / 'bin/id', '#!/bin/sh\necho 0\n')
        executable(root / 'bin/flock', '#!/bin/sh\nexit ' + ('1' if failure == 'lock' else '0') + '\n')
        executable(root / 'bin/sleep', '#!/bin/sh\nexit 0\n')
        executable(root / 'bin/systemctl', '#!/bin/sh\ncase "$1" in is-active|is-enabled) exit ' + ('0' if failure == 'second-active' else '1') + ';; esac\necho "$@" >> "' + str(root / 'calls') + '"\nexit 0\n')
        if failure == 'guard-latch':
            control = root / 'bin/systemctl'
            source = control.read_text().replace('exit 0\n', 'if [ "$1 $2" = "start traffic-guard.service" ]; then touch "' + str(state / 'traffic-stopped') + '"; exit 1; fi\nexit 0\n')
            control.write_text(source)
        executable(root / 'bin/caddy', '#!/bin/sh\nexit ' + ('1' if failure == 'caddy' else '0') + '\n')
        executable(root / 'lib/systemd/system/caddy.service', '[Service]\nExecStart=' + str(root / 'bin/caddy') + '\n')
        executable(root / 'bin/systemd-analyze', '''#!/usr/bin/env python3
import pathlib,sys
for arg in sys.argv[2:]:
 for line in pathlib.Path(arg).read_text().splitlines():
  if line.startswith(('ExecStart=', 'ExecStartPre=')):
   target=line.split('=',1)[1].split()[0].lstrip('+')
   assert pathlib.Path(target).is_file(), target
sys.exit(''' + ('1' if failure == 'unit' else '0') + ')\n')
        copied = root / 'scripts'
        shutil.copytree(INFRA / 'scripts', copied)
        for path in copied.glob('*'):
            if not path.is_file(): continue
            source = path.read_text()
            for prefix in ('/opt/wss-relay', '/var/lib/wss-relay', '/var/lib/lobby-relay', '/var/lock/wss-relay-deploy.lock', '/etc/wss-relay', '/etc/systemd/system', '/etc/caddy', '/lib/systemd/system/caddy.service'):
                source = source.replace(prefix, str(root / prefix.lstrip('/')))
            # Only identity is replaced; file validation and all transaction logic remain real.
            source = source.replace('pwd.getpwnam("wss-relay")', f'pwd.getpwuid({os.getuid()})')
            source = source.replace('info.st_uid != 0', f'info.st_uid != {os.getuid()}')
            path.write_text(source)
        env = {**os.environ, 'PATH': str(root / 'bin') + os.pathsep + os.environ['PATH']}
        result = subprocess.run(['bash', str(copied / 'deploy-backend.sh'), str(archive), SHA, role], env=env, capture_output=True, text=True, timeout=30)
        assert (result.returncode != 0) == bool(failure), (role, failure, result.stdout, result.stderr)
        calls = (root / 'calls').read_text() if (root / 'calls').exists() else ''
        for path, body in live.items():
            assert path.read_bytes() == body, ('live data changed', failure, path)
        assert not (root / 'INJECTED').exists()
        if failure:
            if not first: assert (opt / 'current').resolve() == old
            else: assert not (opt / 'current').exists()
            for path, body in previous.items(): assert path.read_bytes() == body, (failure, path)
            if failure in ('health', 'guard-latch'):
                assert f'stop wss-relay@{role}.service caddy.service' in calls
                if not first and failure == 'health': assert 'previous release restored' in result.stderr
                if failure == 'guard-latch':
                    assert (state / 'traffic-stopped').exists()
                    assert 'reload-or-restart caddy.service' not in calls
                    assert calls.count(f'restart wss-relay@{role}.service') == 1
            else: assert not calls, (failure, calls)
        else:
            assert (opt / 'current').resolve() == opt / 'releases' / SHA
            assert f'restart wss-relay@{role}.service' in calls
            assert 'reload-or-restart caddy.service' in calls
            assert '{$' not in (root / 'etc/caddy/Caddyfile').read_text()
        print('PASS', role, failure or 'promotion', 'first' if first else 'upgrade')


for role in ROLES:
    for failure, first in ((None, False), (None, True), ('health', False), ('health', True), ('guard-latch', False), ('guard-latch', True)):
        scenario(role, failure, first)
for failure in ('lock', 'marker', 'missing-ledger', 'injection', 'unknown', 'duplicate', 'listen', 'tls', 'role',
                'capacity', 'relative-file', 'token-mode', 'token-cr', 'traffic', 'caddy-env', 'second-instance', 'second-active', 'caddy', 'unit',
                'unlisted', 'checksum', 'same-sha', 'traversal', 'absolute', 'symlink', 'hardlink', 'device', 'special', 'count', 'size'):
    scenario('standalone', failure)
scenario('coordinator', 'nodes')
scenario('coordinator', 'invalid-ca')
scenario('relay', 'reserved-ip')

# Probe the real checker with curl responses matching the app's HTTP contract.
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    executable(root / 'curl', '''#!/usr/bin/env python3
import os,sys
url=sys.argv[-1]
code='204' if url.endswith('/readyz') else '401' if ':8091/' in url else '426' if url.endswith('/v2/relay') else '200'
print(os.environ.get('BAD_READY', code) if url.endswith('/readyz') else code, end='')
''')
    for role in ROLES:
        env = {**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH']}
        result = subprocess.run(['bash', str(INFRA / 'scripts/check-deployment.sh'), role], env=env, capture_output=True, text=True)
        assert result.returncode == 0, (result.stdout, result.stderr)
        env['BAD_READY'] = '503'
        result = subprocess.run(['bash', str(INFRA / 'scripts/check-deployment.sh'), role], env=env, capture_output=True, text=True)
        assert result.returncode != 0
print('PASS real health checker: healthz 200, readyz 204, auth 401, upgrade 426; readiness 503 refused')
