#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

UNIT_NAME=guildnet-hostapp.service
BIN="$ROOT/bin/hostapp"
RUN_SCRIPT="$ROOT/scripts/run-hostapp.sh"

if [ ! -x "$RUN_SCRIPT" ]; then
  echo "Expected $RUN_SCRIPT to exist and be executable" >&2
  exit 2
fi

sudo tee /etc/systemd/system/$UNIT_NAME >/dev/null <<UNIT
[Unit]
Description=GuildNet HostApp
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$(id -un)
Group=$(id -gn)
WorkingDirectory=$ROOT
Environment=LISTEN_LOCAL=${LISTEN_LOCAL:-0.0.0.0:8090}
ExecStart=$RUN_SCRIPT
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable $UNIT_NAME
sudo systemctl restart $UNIT_NAME || sudo systemctl start $UNIT_NAME
echo "Installed and started $UNIT_NAME"
