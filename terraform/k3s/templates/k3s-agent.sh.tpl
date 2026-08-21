#!/bin/bash
set -euo pipefail

EXTERNAL_IP=$(curl -sf --retry 5 --retry-connrefused --retry-delay 3 -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip")

# No explicit "agent" positional argument: install.sh selects agent mode
# purely from K3S_URL being set, which is the officially documented pattern
# and avoids any ambiguity in how different install.sh versions handle an
# explicit "agent"/"server" positional.
ARGS="--node-external-ip=$EXTERNAL_IP --node-label ${node_label}"
%{ if node_taint != "" }
ARGS="$ARGS --node-taint ${node_taint}"
%{ endif }

curl -sfL --retry 5 --retry-connrefused --retry-delay 3 https://get.k3s.io | \
  INSTALL_K3S_CHANNEL="${k3s_channel}" \
  K3S_URL="https://${server_ip}:6443" \
  K3S_TOKEN="${node_token}" \
  sh -s - $ARGS
