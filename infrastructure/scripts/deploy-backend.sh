#!/usr/bin/env bash
# Run as root on the prepared host. Restarts discard in-memory matches.
set -euo pipefail
umask 077
[[ $(id -u) == 0 && $# == 3 && $2 =~ ^[a-f0-9]{40}$ && $3 =~ ^(standalone|coordinator|relay)$ ]] || { echo 'usage (root): deploy-backend.sh ARCHIVE FULL_COMMIT_SHA INSTANCE' >&2; exit 2; }
archive=$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve(strict=True))' "$1")
release="/opt/wss-relay/releases/$2"
instance=$3
unit="wss-relay@$instance.service"
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
exec 9>/var/lock/wss-relay-deploy.lock
flock -n 9 || { echo 'another deployment is running' >&2; exit 1; }
[[ ! -e /var/lib/wss-relay/traffic-stopped ]] || { echo 'reconcile cost guard before deployment' >&2; exit 1; }
[[ -f /var/lib/wss-relay/traffic.json ]] || { echo 'explicit accounting initialization required' >&2; exit 1; }
if [[ -e /etc/wss-relay/instance ]]; then
  [[ $(cat /etc/wss-relay/instance) == "$instance" ]] || { echo 'only one instance per host; role changes require manual migration' >&2; exit 1; }
fi
for other in standalone coordinator relay; do
  if [[ $other != "$instance" ]] && { systemctl is-active --quiet "wss-relay@$other.service" || systemctl is-enabled --quiet "wss-relay@$other.service"; }; then
    echo 'another instance is active or enabled; manual migration required' >&2
    exit 1
  fi
done
old=$(python3 -c 'import pathlib; p=pathlib.Path("/opt/wss-relay/current"); print(p.resolve(strict=True) if p.is_symlink() else "")')
stage=$(mktemp -d /opt/wss-relay/releases/.stage.XXXXXX)
mkdir "$stage/payload" "$stage/prepared" "$stage/backup"
changed=false
owned=(/opt/wss-relay/ops/check-traffic-budget.sh /opt/wss-relay/ops/traffic_budget.py
  /etc/systemd/system/wss-relay@.service /etc/systemd/system/traffic-guard.service
  /etc/systemd/system/traffic-guard.timer /etc/systemd/system/caddy.service.d/wss-relay.conf
  /etc/caddy/Caddyfile /etc/wss-relay/instance)
healthy() {
  local target=$1
  for _ in {1..15}; do
    if "$target/scripts/check-deployment.sh" "$instance" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}
switch_current() {
  ln -sfn "$1" /opt/wss-relay/current.next
  python3 -c 'import os; os.replace("/opt/wss-relay/current.next", "/opt/wss-relay/current")'
}
cleanup() {
  code=$?
  trap - EXIT
  if [[ $code != 0 && $changed == true ]]; then
    systemctl stop "$unit" caddy.service || true
    restored=true
    for index in "${!owned[@]}"; do
      if [[ -e $stage/backup/$index ]]; then
        cp -p "$stage/backup/$index" "${owned[$index]}" || restored=false
      else
        rm -f -- "${owned[$index]}" || restored=false
      fi
    done
    if [[ -n $old && -x $old/wss-relay ]]; then
      switch_current "$old" || restored=false
      systemctl daemon-reload || restored=false
      if [[ $restored == true && ! -e /var/lib/wss-relay/traffic-stopped ]] && systemctl restart "$unit" && healthy "$old" && systemctl reload-or-restart caddy.service; then
        echo 'deployment failed; previous release restored (in-memory matches cannot be restored)' >&2
      else
        systemctl stop "$unit" caddy.service || true
        echo 'rollback unavailable or unhealthy; services stopped, administrator recovery required' >&2
      fi
    else
      rm -f /opt/wss-relay/current /opt/wss-relay/current.next
      systemctl daemon-reload || true
      echo 'first deployment failed; instance and Caddy stopped' >&2
    fi
  fi
  rm -rf -- "$stage"
  exit "$code"
}
trap cleanup EXIT
# Validate the complete archive and manifest before executing any packaged tool.
python3 - "$archive" "$stage/payload" <<'PY'
import hashlib
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile
root = Path(sys.argv[2])
with tarfile.open(sys.argv[1]) as tar:
    members = tar.getmembers()
    if len(members) > 30 or sum(m.size for m in members) > 150 * 1024 * 1024:
        raise SystemExit('invalid release size')
    names = set()
    for member in members:
        path = PurePosixPath(member.name)
        if (path.is_absolute() or '..' in path.parts or not (member.isfile() or member.isdir())
                or member.mode & 0o7000 or not re.fullmatch(r'[A-Za-z0-9_@./-]+', member.name)
                or str(path) in names):
            raise SystemExit('invalid archive entry')
        names.add(str(path))
    tar.extractall(root, filter='data')
files = {p.relative_to(root).as_posix() for p in root.rglob('*') if p.is_file()} - {'SHA256SUMS'}
verified = set()
for line in (root / 'SHA256SUMS').read_text().splitlines():
    match = re.fullmatch(r'([a-f0-9]{64})  ([A-Za-z0-9_@./-]+)', line)
    if not match or match[2] not in files or match[2] in verified:
        raise SystemExit('invalid checksum manifest')
    if hashlib.sha256((root / match[2]).read_bytes()).hexdigest() != match[1]:
        raise SystemExit('checksum mismatch')
    verified.add(match[2])
if verified != files:
    raise SystemExit('unchecksummed archive payload')
PY
python3 "$script_dir/deploy-preflight.py" "$stage/payload" "$instance" "$stage/prepared"
chmod 0755 "$stage/payload" "$stage/payload/wss-relay" "$stage/payload/scripts"
if [[ -e $release || -L $release ]]; then
  [[ -d $release && ! -L $release ]] || { echo 'invalid existing release' >&2; exit 1; }
  diff -r "$stage/payload" "$release" >/dev/null || { echo 'release ID already has different content' >&2; exit 1; }
else
  mv "$stage/payload" "$release"
fi
for index in "${!owned[@]}"; do
  if [[ -e ${owned[$index]} || -L ${owned[$index]} ]]; then
    [[ -f ${owned[$index]} && ! -L ${owned[$index]} ]] || { echo 'release-owned destination must be a regular file' >&2; exit 1; }
    cp -p "${owned[$index]}" "$stage/backup/$index"
  fi
done
changed=true
install -d -m 0755 /opt/wss-relay/ops /etc/systemd/system/caddy.service.d
install -m 0755 "$release/scripts/check-traffic-budget.sh" "$release/scripts/traffic_budget.py" /opt/wss-relay/ops/
install -m 0644 "$release/ops/wss-relay@.service" "$release/ops/traffic-guard.service" "$release/ops/traffic-guard.timer" /etc/systemd/system/
install -m 0644 "$release/ops/caddy-wss-relay.conf" /etc/systemd/system/caddy.service.d/wss-relay.conf
install -m 0644 "$stage/prepared/Caddyfile" /etc/caddy/Caddyfile
printf '%s\n' "$instance" > /etc/wss-relay/instance
switch_current "$release"
systemctl daemon-reload
systemctl enable "$unit" >/dev/null
systemctl restart "$unit"
healthy "$release" || { echo 'new server health/readiness check failed' >&2; exit 1; }
systemctl start traffic-guard.service
[[ ! -e /var/lib/wss-relay/traffic-stopped ]] || { echo 'cost guard stopped deployment' >&2; exit 1; }
systemctl enable --now traffic-guard.timer >/dev/null
systemctl enable caddy.service >/dev/null
systemctl reload-or-restart caddy.service
echo "deployed $2 instance=$instance; previous=${old:-none}"
