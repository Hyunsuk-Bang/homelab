#!/usr/bin/env bash
set -euo pipefail

# Shared settings
source ./.env

# Node list: name and last IP octet
nodes=(
  "hbang-px1:82"
#  "hbang-px2:83"
#  "hbang-px3:84"
)

# Function to run make for a single node
build_for_node() {
  local node_name="$1"
  local ip_octet="$2"

  export PROXMOX_URL="${PROXMOX_URL_BASE}.${ip_octet}:8006/api2/json"
  export PROXMOX_NODE="$node_name"

  echo "🚀 Building on $node_name ($PROXMOX_URL)..."
  make build-proxmox-ubuntu-2404
}

export -f build_for_node

# Run in parallel for all nodes
for entry in "${nodes[@]}"; do
  IFS=: read -r name octet <<< "$entry"
  bash -c "build_for_node \"$name\" \"$octet\"" &
done

wait
echo "✅ All builds completed."

