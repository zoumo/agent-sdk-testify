#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 || $1 != /* || ( $2 != /* && $2 != --fetch ) ]]; then
  echo "usage: $0 /absolute/new-build-directory /absolute/install-prefix|--fetch" >&2
  exit 2
fi

source_dir=$(cd "$(dirname "$0")" && pwd)
build_dir=$1
prefix=$2
revision=$(cat "$source_dir/upstream-revision")
archive_sha=d825bdb1b8782d94264eb2b39a0b80a9301ea8dbfd597249358b2f931107b55b

mkdir "$build_dir"
mkdir "$build_dir/upstream" "$build_dir/src"
archive=${CODEX_SOURCE_ARCHIVE:-"$build_dir/codex.tar.gz"}
if [[ -z ${CODEX_SOURCE_ARCHIVE:-} ]]; then
  if [[ ${CARGO_NET_OFFLINE:-false} == true ]]; then
    echo "offline build requires CODEX_SOURCE_ARCHIVE" >&2
    exit 1
  fi
  curl --fail --location --retry 2 \
    "https://codeload.github.com/openai/codex/tar.gz/$revision" -o "$archive"
fi
printf '%s  %s\n' "$archive_sha" "$archive" | shasum -a 256 -c -
tar -xzf "$archive" -C "$build_dir/upstream" --strip-components=1
patch -d "$build_dir/upstream" -p1 < "$source_dir/stdio.patch"
cp "$source_dir/Cargo.toml" "$source_dir/Cargo.lock" "$source_dir/upstream-revision" "$build_dir/"
cp -R "$source_dir/src/." "$build_dir/src/"
host=$(rustc +1.99.1 -vV | sed -n 's/^host: //p')
if [[ $prefix == --fetch ]]; then
  exec cargo +1.99.1 fetch --target "$host" --locked --manifest-path "$build_dir/Cargo.toml"
fi
target_dir=${CARGO_TARGET_DIR:-"$build_dir/target"}
options=(--target "$host" --release --locked --manifest-path "$build_dir/Cargo.toml" --target-dir "$target_dir")
cargo +1.99.1 test "${options[@]}"
cargo +1.99.1 build "${options[@]}"
install -d "$prefix/bin"
install -m 755 "$target_dir/$host/release/agent-sdk-testify-codex" "$prefix/bin/agent-sdk-testify-codex"
