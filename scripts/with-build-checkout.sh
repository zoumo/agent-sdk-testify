#!/usr/bin/env bash
# Go 1.26 does not stamp VCS identity when .git is a worktree file.
set -euo pipefail
source_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
if (( $# == 0 )); then
  echo "usage: bash scripts/with-build-checkout.sh COMMAND [ARG ...]" >&2
  exit 2
fi
cd "$source_root"
export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOWORK=off PIP_NO_INDEX=1
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES

check_source() {
  local repository status
  repository=$(git rev-parse --show-toplevel)
  status=$(git status --porcelain=v1 --untracked-files=all)
  [[ "$repository" == "$source_root" ]] || {
    echo "build source must be the repository root: $source_root" >&2
    exit 1
  }
  [[ -z "$status" ]] || {
    echo "build source is dirty; commit the intended source before building: $source_root" >&2
    exit 1
  }
}
check_source
source_head=$(git rev-parse --verify HEAD)
source_tree=$(git rev-parse 'HEAD^{tree}')
if [[ -d .git ]]; then
  "$@"
  exit $?
fi
[[ -f .git ]] || { echo "build source has no Git checkout" >&2; exit 1; }

build_root=$(mktemp -d "${TMPDIR:-/tmp}/agent-sdk-testify-build.XXXXXXXX")
checkout="$build_root/source"
# shellcheck disable=SC2329 # Invoked by the EXIT trap.
finish() {
  local status=$?
  if (( status == 0 )); then
    rm -rf "$build_root"
  else
    printf 'Build failed (%s); checkout and command log retained: %s\n' "$status" "$build_root" >&2
  fi
}
trap finish EXIT
# Only local source objects are copied. No fetch, new commit, or shared object links.
git clone --quiet --no-hardlinks --no-checkout "$source_root" "$checkout"
git -C "$checkout" checkout --quiet --detach "$source_head"
checkout_status=$(git -C "$checkout" status --porcelain=v1 --untracked-files=all)
[[ -d "$checkout/.git" && "$(git -C "$checkout" rev-parse HEAD)" == "$source_head" &&
   "$(git -C "$checkout" rev-parse 'HEAD^{tree}')" == "$source_tree" &&
   -z "$checkout_status" ]] || {
  echo "independent build checkout does not match the clean source commit/tree" >&2
  exit 1
}
check_source
[[ "$(git rev-parse HEAD)" == "$source_head" && "$(git rev-parse 'HEAD^{tree}')" == "$source_tree" ]] || {
  echo "build source changed while creating the checkout" >&2
  exit 1
}
printf 'Build source: %s\nBuild revision: %s\nBuild tree: %s\nBuild checkout: %s\n' \
  "$source_root" "$source_head" "$source_tree" "$checkout" >&2
printf '%q ' "$@" > "$build_root/command.log"
printf '\n' >> "$build_root/command.log"
cd "$checkout"
set +e
"$@"
status=$?
set -e
printf 'command-exit=%s\n' "$status" >> "$build_root/command.log"
if (( status == 0 )); then
  checkout_status=$(git status --porcelain=v1 --untracked-files=all)
  if [[ "$(git rev-parse HEAD)" != "$source_head" ||
    "$(git rev-parse 'HEAD^{tree}')" != "$source_tree" || -n "$checkout_status" ]]; then
    echo "build command changed the independent source checkout" >&2
    status=1
  fi
fi
printf 'exit=%s\n' "$status" >> "$build_root/command.log"
exit "$status"
