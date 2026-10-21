#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tools_dir="$repo_root/.tools"
go_dir="$tools_dir/go"
buf_bin="$tools_dir/bin/buf"
protoc_bin="$tools_dir/bin/protoc"
protoc_gen_go_bin="$tools_dir/bin/protoc-gen-go"

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "unsupported bootstrap host: expected Darwin/arm64" >&2
  exit 1
fi

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/relay-tools.XXXXXX")
trap 'rm -rf -- "$tmp_dir"' EXIT HUP INT TERM

if [ ! -x "$go_dir/bin/go" ] || ! "$go_dir/bin/go" version | grep -Fxq 'go version go1.26.8 darwin/arm64'; then
  curl -fL --retry 3 -o "$tmp_dir/go.tar.gz" https://go.dev/dl/go1.26.8.darwin-arm64.tar.gz
  printf '%s  %s\n' a012b25b571bd0138a03dcd25375ceba866fe5ca822f426d2c66a4de56fd3f4b "$tmp_dir/go.tar.gz" | shasum -a 256 -c -
  tar -C "$tmp_dir" -xzf "$tmp_dir/go.tar.gz"
  rm -rf -- "$go_dir"
  mkdir -p "$tools_dir"
  mv "$tmp_dir/go" "$go_dir"
fi

if [ ! -x "$buf_bin" ] || [ "$("$buf_bin" --version)" != "1.72.0" ]; then
  curl -fL --retry 3 -o "$tmp_dir/buf" https://github.com/bufbuild/buf/releases/download/v1.72.0/buf-Darwin-arm64
  printf '%s  %s\n' 5176f23a6118b9978de1340c3e3301a4ed0d48e16a669510be44b4c355170d57 "$tmp_dir/buf" | shasum -a 256 -c -
  mkdir -p "$(dirname "$buf_bin")"
  install -m 0755 "$tmp_dir/buf" "$buf_bin"
fi

if [ ! -x "$protoc_bin" ] || [ "$("$protoc_bin" --version)" != "libprotoc 35.1" ]; then
  curl -fL --retry 3 -o "$tmp_dir/protoc.zip" https://github.com/protocolbuffers/protobuf/releases/download/v35.1/protoc-35.1-osx-aarch_64.zip
  printf '%s  %s\n' 193289af0470c6a1aada357d4fba0bbf8d78bfaac8b5e42ca30af2ef75583de2 "$tmp_dir/protoc.zip" | shasum -a 256 -c -
  unzip -q "$tmp_dir/protoc.zip" -d "$tmp_dir/protoc"
  mkdir -p "$(dirname "$protoc_bin")"
  install -m 0755 "$tmp_dir/protoc/bin/protoc" "$protoc_bin"
fi

if [ ! -x "$protoc_gen_go_bin" ] || [ "$("$protoc_gen_go_bin" --version)" != "protoc-gen-go v1.36.11" ]; then
  mkdir -p "$tmp_dir/go-bin" "$repo_root/.cache/go-mod"
  GOBIN="$tmp_dir/go-bin" \
    GOCACHE="$tmp_dir/go-build-cache" \
    GOMODCACHE="$repo_root/.cache/go-mod" \
    "$go_dir/bin/go" install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
  mkdir -p "$(dirname "$protoc_gen_go_bin")"
  install -m 0755 "$tmp_dir/go-bin/protoc-gen-go" "$protoc_gen_go_bin"
fi

"$go_dir/bin/go" version
"$buf_bin" --version
"$protoc_bin" --version
"$protoc_gen_go_bin" --version
