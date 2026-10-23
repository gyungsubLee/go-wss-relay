#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 && $1 =~ ^(standalone|coordinator|relay)$ ]] || { echo 'usage: check-deployment.sh INSTANCE' >&2; exit 2; }
probe() {
  local code
  code=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 2 --max-time 6 "$2") || return 1
  [[ $code == "$1" ]] || { echo "unexpected status ${code:-none} from $2" >&2; return 1; }
}
probe 200 http://127.0.0.1:8090/healthz
probe 204 http://127.0.0.1:8090/readyz
if [[ $1 == coordinator ]]; then
  probe 401 http://127.0.0.1:8091/v2/nodes
else
  probe 401 http://127.0.0.1:8091/v2/status
fi
if [[ $1 != coordinator ]]; then
  probe 426 http://127.0.0.1:8090/v2/relay
fi
echo healthy
