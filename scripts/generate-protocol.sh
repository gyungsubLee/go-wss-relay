#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repo_root"
mode=${1:-generate}
case "$mode" in
  generate|check) ;;
  *) echo "usage: $0 [generate|check]" >&2; exit 1 ;;
esac

mkdir -p .cache
stage=$(mktemp -d "$repo_root/.cache/proto-generate.XXXXXX")
trap 'rm -rf -- "$stage"' 0 1 2 15

# Both plugins must finish before either tracked output can change.
.tools/bin/buf generate --output "$stage"
test -s "$stage/gen/go/relay/v2/relay.pb.go"
test -s "$stage/client/src/relay/gen/relay/v2/relay_pb.ts"

for output in gen/go/relay client/src/relay/gen/relay; do
  if [ "$mode" = check ]; then
    diff -ru "$output" "$stage/$output"
  else
    mkdir -p "$output/v2"
    cp "$stage/$output/v2/"* "$output/v2/"
  fi
done
