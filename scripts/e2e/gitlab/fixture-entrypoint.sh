#!/usr/bin/env bash
set -euo pipefail

restore_dir="/opt/kenn-forge/gitlab-fixture/data"
sentinel="/var/opt/gitlab/.kenn-forge-fixture-restored"

if [ -d "$restore_dir" ] && [ ! -f "$sentinel" ]; then
  if [ -f "$restore_dir/etc-gitlab.tgz" ]; then
    tar -xzf "$restore_dir/etc-gitlab.tgz" -C /etc/gitlab
  fi
  if [ -f "$restore_dir/var-opt-gitlab.tgz" ]; then
    tar -xzf "$restore_dir/var-opt-gitlab.tgz" -C /var/opt/gitlab
  fi
  touch "$sentinel"
fi

exec /assets/init-container
