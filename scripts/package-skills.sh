#!/bin/sh
# Ship the exact same files embedded by internal/skills.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$repo_dir/build"
tar -czf "$repo_dir/build/vecdex-skills.tar.gz" -C "$repo_dir/internal/skills/bundle" \
  memory-use memory-maintenance memory-handoff memory-administration
printf '%s\n' "$repo_dir/build/vecdex-skills.tar.gz"
