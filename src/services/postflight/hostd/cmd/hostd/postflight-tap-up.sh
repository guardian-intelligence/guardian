#!/usr/bin/env bash
set -euo pipefail

tap="${1:?QEMU did not supply a tap interface}"
bridge_name="${HOSTD_GUEST_BRIDGE:?HOSTD_GUEST_BRIDGE is required}"

[[ "$tap" =~ ^pft[0-9a-f]{12}$ ]]
/usr/sbin/ip link show dev "$bridge_name" >/dev/null
/usr/sbin/ip link set dev "$tap" master "$bridge_name"
/usr/sbin/ip link set dev "$tap" up
/usr/sbin/bridge link set dev "$tap" isolated on
