#!/usr/bin/env python3
"""Validate the documented narrow deployment format without starting the app."""
import base64
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import ssl
import stat
import subprocess
import sys
from urllib.parse import urlsplit

sys.dont_write_bytecode = True
ROLES = ('standalone', 'coordinator', 'relay')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_file(path, owner, maximum=1024 * 1024):
    path = Path(path)
    require(path.is_absolute(), 'configuration file paths must be absolute')
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not info.st_mode & 0o022, 'config must be a protected regular file')
    for directory in path.parents:
        entry = directory.stat()
        mode = (entry.st_mode >> (6 if entry.st_uid == owner.pw_uid else 3 if entry.st_gid == owner.pw_gid else 0)) & 7
        require(mode & 1, 'service user cannot traverse config directory')
    mode = (info.st_mode >> (6 if info.st_uid == owner.pw_uid else 3 if info.st_gid == owner.pw_gid else 0)) & 7
    require(mode & 4, 'service user cannot read config')
    with path.open('rb') as source:
        require(os.path.samestat(info, os.fstat(source.fileno())), 'config changed during read')
        body = source.read(maximum + 1)
    require(len(body) <= maximum and os.path.samestat(info, path.lstat()), 'config size or replacement rejected')
    return body


def token(path, owner):
    body = read_file(path, owner, 45)
    info = Path(path).lstat()
    require(stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == owner.pw_uid and info.st_nlink == 1, 'token requires service-owned single-link 0600 file')
    if body.endswith(b'\r\n'):
        body = body[:-2]
    elif body.endswith(b'\n'):
        body = body[:-1]
    require(re.fullmatch(rb'[A-Za-z0-9_-]{43}', body), 'invalid token encoding')
    decoded = base64.urlsafe_b64decode(body + b'=')
    require(any(decoded) and base64.urlsafe_b64encode(decoded).rstrip(b'=') == body, 'invalid canonical token')


def assignments(path, owner):
    values = {}
    for line in read_file(path, owner, 16384).decode('utf-8').splitlines():
        if not line or line.startswith('#'):
            continue
        key, separator, value = line.partition('=')
        require(separator and re.fullmatch('[A-Z_]+', key) and key not in values, 'invalid env assignment')
        require(value and re.fullmatch(r'[A-Za-z0-9_./:@,=+%\[\] -]+', value), 'env quoting/expansion is unsupported')
        values[key] = value
    return values


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'duplicate JSON key unsupported')
        result[key] = value
    return result


def read_json(path, owner, maximum):
    value = json.loads(read_file(path, owner, maximum), object_pairs_hook=object_pairs)
    require(type(value) is dict, 'JSON object required')
    return value


def safe_url(value, websocket=False):
    require(type(value) is str, 'URL must be a string')
    url = urlsplit(value)
    require(url.scheme == ('wss' if websocket else 'https') and url.hostname and url.username is None
            and url.password is None and not url.query and not url.fragment and '?' not in value and '#' not in value
            and url.path in (('/v2/relay',) if websocket else ('', '/')), 'invalid deployment URL')
    if url.port is not None:
        require(1 <= url.port <= 65535, 'invalid URL port')
    return url


def validate(stage, instance, prepared):
    owner = pwd.getpwnam("wss-relay")
    etc = Path('/etc/wss-relay')
    values = assignments(etc / f'{instance}.env', owner)
    require(set(values) == {'RELAY_ARGS'}, 'instance env requires only RELAY_ARGS')
    words = values['RELAY_ARGS'].split()
    require(len(words) % 2 == 0, 'arguments require --flag value pairs')
    strings = {'role', 'nodes-config', 'listen', 'management-listen', 'public-url', 'traffic-config', 'node-id', 'operator-token-file'}
    limits = {'max-rooms': 4096, 'max-participants': 8192, 'handshakes-per-ip': 20000}
    repeated = {'trusted-proxy', 'allowed-origin'}
    args = {}
    for flag, value in zip(words[::2], words[1::2]):
        require(flag.startswith('--'), 'expected --flag')
        key = flag[2:]
        require(key in strings | limits.keys() | repeated, 'unsupported deployment flag')
        require(key in repeated or key not in args, 'duplicate singleton flag')
        if key == 'trusted-proxy':
            network = ipaddress.ip_network(value, strict=False)
            require(network.prefixlen > 0 and network.is_loopback, 'deployment trusts loopback proxy peers only')
        if key == 'allowed-origin':
            require(value != '*' and not any(c.isspace() for c in value), 'invalid origin')
        if key in limits:
            require(re.fullmatch(r'[0-9]+', value) and 0 <= int(value) <= limits[key], 'invalid relay limit')
        args[key] = value
    require(args.get('role') == instance, 'instance and role must match')
    require(args.get('listen', '127.0.0.1:8090') == '127.0.0.1:8090'
            and args.get('management-listen', '127.0.0.1:8091') == '127.0.0.1:8091', 'Caddy requires fixed plain loopback listeners')
    require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', args.get('node-id', 'local')), 'invalid node ID')
    require('operator-token-file' in args, 'operator-token-file required')
    token(args['operator-token-file'], owner)
    if 'traffic-config' in args:
        traffic = read_json(args['traffic-config'], owner, 4096)
        fields = {'nodeIngressBytesPerSecond': 16384, 'nodeEgressBytesPerSecond': 131072,
                  'roomIngressBytesPerSecond': 16384, 'roomEgressBytesPerSecond': 131072}
        require(traffic.keys() <= fields.keys(), 'unknown traffic field')
        for key, value in traffic.items():
            require(type(value) is int and (value == 0 or fields[key] <= value <= 2**63 - 1), 'invalid traffic budget')
    if instance == 'coordinator':
        require('nodes-config' in args, 'nodes-config required')
        config = read_json(args['nodes-config'], owner, 1024 * 1024)
        require(set(config) == {'nodes'} and type(config['nodes']) is list and 1 <= len(config['nodes']) <= 64, '1..64 nodes required')
        ids = set()
        for node in config['nodes']:
            require(type(node) is dict and node.keys() <= {'id', 'controlUrl', 'websocketUrl', 'tokenFile', 'caFile'}, 'invalid node fields')
            require(all(type(node.get(key)) is str for key in ('id', 'controlUrl', 'websocketUrl', 'tokenFile')), 'node strings required')
            require(re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,63}', node['id']) and node['id'] not in ids, 'invalid or duplicate node ID')
            ids.add(node['id'])
            safe_url(node['controlUrl'])
            safe_url(node['websocketUrl'], True)
            token(node['tokenFile'], owner)
            if node.get('caFile'):
                pem = read_file(node['caFile'], owner).decode('ascii')
                ssl.create_default_context(cadata=pem)
    caddy = assignments(etc / 'caddy.env', owner)
    domain_key = instance.upper() + '_DOMAIN'
    required = {domain_key}
    if instance == 'relay':
        required |= {'NODE_CONTROL_DOMAIN', 'NODE_PRIVATE_IP', 'COORDINATOR_PRIVATE_IP'}
    for key in required:
        require(key in caddy, 'missing Caddy value')
        if key.endswith('_IP'):
            address = ipaddress.IPv4Address(caddy[key])
            require(any(address in ipaddress.ip_network(network) for network in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16')), 'RFC1918 private IPv4 required')
        else:
            require(len(caddy[key]) <= 253 and all(re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', label) for label in caddy[key].split('.')), 'invalid Caddy domain')
    if instance != 'coordinator':
        require(args.get('public-url') == f"wss://{caddy[domain_key]}/v2/relay", 'public URL must match Caddy domain')
        safe_url(args['public-url'], True)
    content = (stage / 'ops' / f'Caddyfile.{instance}').read_text()
    for key in required:
        content = content.replace('{$' + key + '}', caddy[key])
    require('{$' not in content, 'unresolved Caddy environment')
    (prepared / 'Caddyfile').write_text(content)
    subprocess.run(['caddy', 'validate', '--adapter', 'caddyfile', '--config', str(prepared / 'Caddyfile')], check=True)
    # Verify the actual staged executable and guard before current exists on first install.
    units = []
    for name in ('wss-relay@.service', 'traffic-guard.service', 'traffic-guard.timer'):
        content = (stage / 'ops' / name).read_text()
        content = content.replace('/opt/wss-relay/current/wss-relay', str(stage / 'wss-relay'))
        content = content.replace('/opt/wss-relay/ops/', str(stage / 'scripts') + '/')
        target = prepared / name.replace('@.', '@' + instance + '.')
        target.write_text(content)
        units.append(str(target))
    require(not Path('/etc/systemd/system/caddy.service').exists(), 'custom Caddy unit requires manual integration')
    dropins = Path('/etc/systemd/system/caddy.service.d')
    require(all(path.name == 'wss-relay.conf' for path in dropins.glob('*.conf')), 'custom Caddy drop-ins require manual integration')
    content = Path('/lib/systemd/system/caddy.service').read_text()
    content += '\n' + (stage / 'ops/caddy-wss-relay.conf').read_text()
    content = content.replace('/opt/wss-relay/ops/', str(stage / 'scripts') + '/')
    target = prepared / 'caddy.service'
    target.write_text(content)
    units.append(str(target))
    subprocess.run(['systemd-analyze', 'verify', *units], check=True)
    traffic = assignments(etc / 'traffic.env', owner)
    require(set(traffic) == {'WSS_INSTANCE', 'TRAFFIC_INTERFACE', 'TRAFFIC_WARN_BYTES', 'TRAFFIC_STOP_BYTES'}
            and traffic['WSS_INSTANCE'] == instance, 'traffic env must select the same instance')
    require(re.fullmatch(r'[A-Za-z0-9_.-]{1,15}', traffic['TRAFFIC_INTERFACE']) and traffic['TRAFFIC_INTERFACE'] != 'lo', 'external interface required')
    warn, stop = (int(traffic[key]) for key in ('TRAFFIC_WARN_BYTES', 'TRAFFIC_STOP_BYTES'))
    require(0 < warn < stop < 3_000_000_000_000, 'invalid monthly limits')
    # Keep successful accounting durable; rollback must never rewind this ledger.
    subprocess.run([str(stage / 'scripts/check-traffic-budget.sh')], env={**os.environ, **traffic}, check=True)


if __name__ == '__main__':
    try:
        validate(Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]))
    except Exception as error:
        # File contents and credentials must not enter diagnostics.
        print(f'deployment preflight failed ({type(error).__name__}); inspect protected configuration', file=sys.stderr)
        raise SystemExit(1)
