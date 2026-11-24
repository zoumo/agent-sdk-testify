#!/usr/bin/env bash
set -euo pipefail

if [[ $# != 2 || $1 != /* || $2 != /* ]]; then
  echo "usage: $0 /absolute/new-build-directory /absolute/install-prefix" >&2
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
  curl --fail --location --retry 2 \
    "https://codeload.github.com/openai/codex/tar.gz/$revision" -o "$archive"
fi
printf '%s  %s\n' "$archive_sha" "$archive" | shasum -a 256 -c -
tar -xzf "$archive" -C "$build_dir/upstream" --strip-components=1
patch -d "$build_dir/upstream" -p1 < "$source_dir/stdio.patch"
cp "$source_dir/Cargo.toml" "$source_dir/Cargo.lock" "$source_dir/upstream-revision" "$build_dir/"
cp "$source_dir/src/main.rs" "$build_dir/src/"
cargo +1.99.1 build --locked --manifest-path "$build_dir/Cargo.toml" --target-dir "$build_dir/target"
install -d "$prefix/bin"
install -m 755 "$build_dir/target/debug/agent-sdk-testify-codex" "$prefix/bin/agent-sdk-testify-codex"
