#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
tools_dir="$repo_root/.tools"
go_dir="$tools_dir/go"
buf_bin="$tools_dir/bin/buf"
protoc_bin="$tools_dir/bin/protoc"
protoc_gen_go_bin="$tools_dir/bin/protoc-gen-go"

case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)
    go_platform=darwin-arm64
    go_host=darwin/arm64
    go_sha=0b5183492292ada170f9b561a834863808dd02edba599e4ad0e89899330020b3
    buf_platform=Darwin-arm64
    buf_sha=5176f23a6118b9978de1340c3e3301a4ed0d48e16a669510be44b4c355170d57
    protoc_platform=osx-aarch_64
    protoc_sha=193289af0470c6a1aada357d4fba0bbf8d78bfaac8b5e42ca30af2ef75583de2
    ;;
  Linux/x86_64)
    go_platform=linux-amd64
    go_host=linux/amd64
    go_sha=ccfe7ae822d2c2f41f4fe7c956199967a4dac22d0ee0d2b2bfdb440aab3b20c5
    buf_platform=Linux-x86_64
    buf_sha=8720830e26a733da55bb89bcd3cb44849c0965fc0c44fb5d691cccdc64dca5af
    protoc_platform=linux-x86_64
    protoc_sha=6930ebf62bd4ea607b98fff052596c6ee564b9835b4ce172c75a3f53ae9d91b7
    ;;
  *)
    echo "unsupported bootstrap host: expected Darwin/arm64 or Linux/x86_64" >&2
    exit 1
    ;;
esac

verify_checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s  %s\n' "$1" "$2" | sha256sum -c -
  else
    printf '%s  %s\n' "$1" "$2" | shasum -a 256 -c -
  fi
}

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/relay-tools.XXXXXX")
trap 'rm -rf -- "$tmp_dir"' EXIT HUP INT TERM

if [ ! -x "$go_dir/bin/go" ] || ! GOTOOLCHAIN=local "$go_dir/bin/go" version | grep -Fxq "go version go1.26.10 $go_host"; then
  curl -fL --retry 3 -o "$tmp_dir/go.tar.gz" "https://go.dev/dl/go1.26.10.$go_platform.tar.gz"
  verify_checksum "$go_sha" "$tmp_dir/go.tar.gz"
  tar -C "$tmp_dir" -xzf "$tmp_dir/go.tar.gz"
  rm -rf -- "$go_dir"
  mkdir -p "$tools_dir"
  mv "$tmp_dir/go" "$go_dir"
fi

if [ ! -x "$buf_bin" ] || [ "$("$buf_bin" --version)" != "1.72.0" ]; then
  curl -fL --retry 3 -o "$tmp_dir/buf" "https://github.com/bufbuild/buf/releases/download/v1.72.0/buf-$buf_platform"
  verify_checksum "$buf_sha" "$tmp_dir/buf"
  mkdir -p "$(dirname "$buf_bin")"
  install -m 0755 "$tmp_dir/buf" "$buf_bin"
fi

if [ ! -x "$protoc_bin" ] || [ "$("$protoc_bin" --version)" != "libprotoc 35.1" ]; then
  curl -fL --retry 3 -o "$tmp_dir/protoc.zip" "https://github.com/protocolbuffers/protobuf/releases/download/v35.1/protoc-35.1-$protoc_platform.zip"
  verify_checksum "$protoc_sha" "$tmp_dir/protoc.zip"
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
