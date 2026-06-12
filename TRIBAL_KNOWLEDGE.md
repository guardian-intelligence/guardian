# Tribal knowledge

site.yaml pins physical facts about the box (NIC MAC, NVMe serials, addressing) — when you reprovision or swap hardware, re-derive them from `talosctl get links/disks --insecure` or the Latitude API before running `up`; never identify a NIC by name, because names are kernel-policy trivia and a dangling one boots the node network-dark (2026-06-10).
