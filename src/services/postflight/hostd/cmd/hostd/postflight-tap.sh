#!/usr/bin/env bash
set -euo pipefail

operation="${1:?tap operation is required}"
tap="${2:?tap interface is required}"

[[ "$tap" =~ ^pft[0-9a-f]{12}$ ]]

case "$operation" in
up)
  bridge_name="${HOSTD_GUEST_BRIDGE:?HOSTD_GUEST_BRIDGE is required}"
  /usr/sbin/ip link show dev "$bridge_name" >/dev/null
  if ! /usr/sbin/ip link show dev "$tap" >/dev/null 2>&1; then
    /usr/sbin/ip tuntap add dev "$tap" mode tap
  fi
  /usr/sbin/ip link set dev "$tap" master "$bridge_name"
  /usr/sbin/ip link set dev "$tap" up
  /usr/sbin/bridge link set dev "$tap" isolated on
  ;;
down)
  if /usr/sbin/ip link show dev "$tap" >/dev/null 2>&1; then
    /usr/sbin/ip link delete dev "$tap"
  fi
  ;;
*)
  printf 'unsupported tap operation: %s\n' "$operation" >&2
  exit 2
  ;;
esac
