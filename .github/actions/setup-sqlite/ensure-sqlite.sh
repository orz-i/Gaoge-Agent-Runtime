#!/usr/bin/env bash
set -euo pipefail

installed() {
  local status
  status="$(dpkg-query --show --showformat='${Status}' libsqlite3-dev 2>/dev/null)" || return 1
  [[ "$status" == 'install ok installed' ]]
}

if installed; then
  echo 'libsqlite3-dev is already installed; skipping apt update and install.'
  exit 0
fi

sudo apt-get update
sudo apt-get install -y --no-install-recommends libsqlite3-dev
if ! installed; then
  echo 'libsqlite3-dev is not installed after apt completed.' >&2
  exit 1
fi
