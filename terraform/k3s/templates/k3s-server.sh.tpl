#!/bin/bash
set -euo pipefail

EXTERNAL_IP=$(curl -sf --retry 5 --retry-connrefused --retry-delay 3 -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip")

# k3s ships Traefik as a built-in HelmChart (kube-system/traefik), left
# enabled here (the test suite uses it as the cluster's ingress controller
# instead of installing ingress-nginx). A HelmChartConfig dropped into the
# auto-deploy manifests directory before k3s starts is k3s's documented way
# to customize a built-in chart's values; patching the resulting Deployment
# directly after the fact would just get reverted by k3s's helm-controller
# on its next reconcile.
#
# Single replica, pinned to the control-plane (server) node: it's the one
# node guaranteed to exist and stay up for the cluster's whole lifetime, and
# (unlike a typical HA control plane) this node carries no NoSchedule taint,
# so no toleration is needed.
sudo mkdir -p /var/lib/rancher/k3s/server/manifests
cat <<'EOF' | sudo tee /var/lib/rancher/k3s/server/manifests/traefik-config.yaml >/dev/null
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: traefik
  namespace: kube-system
spec:
  valuesContent: |-
    deployment:
      replicas: 1
    nodeSelector:
      node-role.kubernetes.io/control-plane: "true"
EOF

# No K3S_URL set, so install.sh defaults to server mode; no explicit
# "server" positional needed (see k3s-agent.sh.tpl for why we avoid it).
curl -sfL --retry 5 --retry-connrefused --retry-delay 3 https://get.k3s.io | \
  INSTALL_K3S_CHANNEL="${k3s_channel}" \
  sh -s - \
  --node-external-ip="$EXTERNAL_IP" \
  --node-label "${node_label}" \
  --write-kubeconfig-mode 644
