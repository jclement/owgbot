#!/bin/sh
# Deploys dist/owgbot-linux-arm64 to the Pi. The binary installs itself
# (`owgbot install` carries the systemd unit and config template embedded),
# so this script is just: copy, run install, done.
#
# Reads DEPLOY_HOST / DEPLOY_USER from the environment — set them in
# mise.local.toml (gitignored):
#
#   [env]
#   DEPLOY_HOST = "owgbot.local"
#   DEPLOY_USER = "pi"
#
# The remote user needs passwordless sudo.
set -eu

: "${DEPLOY_HOST:?set DEPLOY_HOST in mise.local.toml}"
DEPLOY_USER="${DEPLOY_USER:-pi}"
target="$DEPLOY_USER@$DEPLOY_HOST"

echo "==> copying binary to $target"
scp dist/owgbot-linux-arm64 "$target:/tmp/owgbot"

echo "==> installing on $DEPLOY_HOST"
ssh "$target" 'sudo /tmp/owgbot install && rm -f /tmp/owgbot && systemctl --no-pager --lines 5 status owgbot'

echo "==> deployed. logs: ssh $target journalctl -u owgbot -f"
