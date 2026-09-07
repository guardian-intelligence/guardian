#!/usr/bin/env python3
"""Render and reconcile the small, first-party Postflight Turbo host."""

import argparse
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile


CONFIG = Path("/etc/postflight")
LIBEXEC = Path("/usr/local/libexec/postflight-host")
BLOCKED_IPV4 = (
    "0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
    "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
    "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
    "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
)


def load_manifest(path):
    m = json.loads(Path(path).read_text())
    for key in ("host_id", "pool", "bridge"):
        if not re.fullmatch(r"[a-z][a-z0-9_-]{0,30}", m[key]):
            raise ValueError(f"invalid {key}")
    if len(m["bridge"]) > 15:
        raise ValueError("bridge exceeds IFNAMSIZ")
    if m["class"] != "postflight-4vcpu-ubuntu24-turbo":
        raise ValueError("this provisioner only serves the first-party Turbo class")
    # These persistent UFW rules have a fixed first-party interface identity.
    # Reject a rename instead of leaving stale allows on an unguarded bridge.
    if m.get("host_firewall") != "ufw" or (m["bridge"], m["subnet"], m["gateway"]) != (
            "pfbr0", "10.77.0.0/24", "10.77.0.1"):
        raise ValueError("unsupported first-party host firewall identity")
    for key in ("slots", "cpus", "memory_mib", "vdev_gib"):
        if type(m[key]) is not int or m[key] <= 0:
            raise ValueError(f"invalid {key}")
    if m["cpus"] != 4 or m["memory_mib"] != 16384:
        raise ValueError("host dimensions disagree with the 4vCPU/16GiB class")
    subnet = ipaddress.IPv4Network(m["subnet"])
    if subnet.prefixlen != 24 or not subnet.is_private:
        raise ValueError("guest network must be a private IPv4 /24")
    addresses = [ipaddress.IPv4Address(m[k]) for k in ("gateway", "dhcp_start", "dhcp_end")]
    if any(a not in subnet or a in (subnet.network_address, subnet.broadcast_address) for a in addresses):
        raise ValueError("guest addresses must be usable members of the subnet")
    if addresses[1] > addresses[2] or addresses[1] <= addresses[0] <= addresses[2]:
        raise ValueError("DHCP range overlaps gateway or is reversed")
    if m["control_plane"] != "https://guardianintelligence.org":
        raise ValueError("unexpected first-party control plane")
    if m["vdev_file"] != "/var/lib/postflight-pool/pool.img":
        raise ValueError("unexpected pool backing path")
    if m["qemu_path"] != "/usr/bin/qemu-system-x86_64" or m["firmware"] != "/usr/share/seabios/bios.bin":
        raise ValueError("unexpected Turbo QEMU or firmware path")
    if not m["qemu_version"].startswith("QEMU emulator version ") or "\n" in m["qemu_version"]:
        raise ValueError("invalid exact QEMU version pin")
    return m


def nft_rules(m, existing=()):
    # Delete/recreate only our tables in ONE transaction. Never flush ruleset:
    # Docker, the host firewall, and unrelated workloads retain their policy.
    delete = "".join(f"delete table {family} postflight_ci\n" for family in existing)
    bridge, subnet, gateway = m["bridge"], m["subnet"], m["gateway"]
    return delete + f"""table inet postflight_ci {{
  set blocked_ipv4 {{ type ipv4_addr; flags interval; elements = {{ {', '.join(BLOCKED_IPV4)} }}; }}
  chain input {{
    type filter hook input priority -20; policy accept;
    iifname "{bridge}" meta nfproto ipv6 drop
    iifname "{bridge}" udp sport 68 udp dport 67 accept
    iifname "{bridge}" ip saddr {subnet} ip daddr {gateway} udp dport 53 accept
    iifname "{bridge}" ip saddr {subnet} ip daddr {gateway} tcp dport {{ 53, 8480 }} accept
    iifname "{bridge}" drop
  }}
  chain forward {{
    type filter hook forward priority -20; policy accept;
    iifname "{bridge}" meta nfproto ipv6 drop
    oifname "{bridge}" meta nfproto ipv6 drop
    iifname "{bridge}" oifname "{bridge}" drop
    iifname "{bridge}" ip saddr != {subnet} drop
    iifname "{bridge}" ip daddr @blocked_ipv4 drop
    iifname "{bridge}" ct state invalid drop
    iifname "{bridge}" accept
    oifname "{bridge}" ct state established,related accept
    oifname "{bridge}" drop
  }}
  chain postrouting {{
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr {subnet} oifname != "{bridge}" masquerade
  }}
}}
table bridge postflight_ci {{
  chain input {{
    type filter hook input priority -20; policy accept;
    iifname "pft*" ether type ip6 drop
  }}
  chain forward {{
    type filter hook forward priority -20; policy accept;
    iifname "pft*" oifname "pft*" drop
    iifname "pft*" ether type ip6 drop
    oifname "pft*" ether type ip6 drop
  }}
}}
"""


def ufw_rules(m):
    bridge, subnet, gateway = m["bridge"], m["subnet"], m["gateway"]
    # Explicit IPv4 wildcards avoid also adding IPv6 rules. DHCP must include
    # source 0.0.0.0 before the guest has a lease. Never allow all host input.
    rules = [
        ("allow", "in", "on", bridge, "proto", "udp", "from", "0.0.0.0/0", "port", "68",
         "to", "0.0.0.0/0", "port", "67", "comment", "postflight-ci-dhcp"),
    ]
    for protocol, port, name in (("udp", "53", "dns-udp"), ("tcp", "53", "dns-tcp"), ("tcp", "8480", "checkout")):
        rules.append(("allow", "in", "on", bridge, "proto", protocol, "from", subnet,
                      "to", gateway, "port", port, "comment", "postflight-ci-" + name))
    # Our earlier nft hook already rejects private/reserved destinations,
    # spoofed sources, guest-to-guest traffic, and non-established ingress.
    # UFW's later default DROP must permit the surviving IPv4 packets too.
    rules.extend([
        ("route", "allow", "in", "on", bridge, "from", subnet, "to", "0.0.0.0/0",
         "comment", "postflight-ci-egress"),
        ("route", "allow", "out", "on", bridge, "from", "0.0.0.0/0", "to", subnet,
         "comment", "postflight-ci-return"),
    ])
    return rules


def ensure_ufw(m):
    if not shutil.which("ufw"):
        raise ValueError("missing provisioned ufw; no unpinned package installation is attempted")
    result = command("ufw", "status", env={**os.environ, "LC_ALL": "C"})
    if "Status: active" not in result.stdout.splitlines():
        raise ValueError("declared UFW firewall is not active; refusing to enable or replace host policy")
    for rule in ufw_rules(m):
        # UFW deduplicates equivalent rules and persists them itself. Do not
        # reset/reload UFW or change any default or existing rule; UFW owns
        # the update to its tables.
        command("ufw", *rule)


def render(m, image_id, criu_version):
    if not re.fullmatch(r"noble-turbo-[a-z0-9-]+", image_id):
        raise ValueError("image must be a secretless noble-turbo golden image")
    if not re.fullmatch(r"[0-9]+(?:\.[0-9]+)+", criu_version):
        raise ValueError("invalid CRIU version")
    env = {
        "HOSTD_HOST_ID": m["host_id"], "HOSTD_CLASS": m["class"],
        "HOSTD_SYNC_URL": m["control_plane"], "HOSTD_POOL": m["pool"] + "/postflight",
        "HOSTD_STATE_DIR": "/var/lib/postflight", "HOSTD_IMAGE_ID": image_id,
        "HOSTD_HOST_SECRET_FILE": "/etc/postflight/host.key",
        "HOSTD_SLOTS": m["slots"], "HOSTD_CPUS": m["cpus"], "HOSTD_MEMORY_MIB": m["memory_mib"],
        "HOSTD_STORAGE_MIN_AVAILABLE_BYTES": 64 << 30,
        "HOSTD_QEMU_PATH": m["qemu_path"], "HOSTD_FIRMWARE_PATH": m["firmware"],
        "HOSTD_CRIU_VERSION": f"Version: {criu_version}",
        "HOSTD_GUEST_NETWORK": "tap", "HOSTD_GUEST_BRIDGE": m["bridge"],
        "HOSTD_TAP_LIFECYCLE_PATH": "/usr/local/libexec/postflight-tap",
        "HOSTD_CHECKOUT_LISTEN_ADDR": m["gateway"] + ":8480",
        "HOSTD_CHECKOUT_GUEST_ORIGIN": "http://" + m["gateway"] + ":8480",
        "HOSTD_WARM_TEMPLATE_DIR": "/var/lib/postflight/warm-templates",
    }
    files = {"hostd.env": "".join(f'{key}="{value}"\n' for key, value in env.items())}
    files["dnsmasq.conf"] = f"""interface={m['bridge']}
listen-address={m['gateway']}
bind-interfaces
no-resolv
server=1.1.1.1
server=9.9.9.9
domain-needed
bogus-priv
dhcp-authoritative
dhcp-range={m['dhcp_start']},{m['dhcp_end']},255.255.255.0,12h
dhcp-option=3,{m['gateway']}
dhcp-option=6,{m['gateway']}
dhcp-leasefile=/var/lib/misc/postflight-dnsmasq.leases
"""
    files["postflight-storage.service"] = """[Unit]
Description=Import and unlock Postflight encrypted storage
After=zfs-import.target
Before=hostd.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/python3 /usr/local/libexec/postflight-host/host.py storage --manifest /etc/postflight/host.json
[Install]
WantedBy=multi-user.target
"""
    files["postflight-network.service"] = """[Unit]
Description=Postflight guest bridge and egress firewall
Wants=network-online.target
After=network-online.target ufw.service
Before=postflight-dnsmasq.service hostd.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/python3 /usr/local/libexec/postflight-host/host.py network --manifest /etc/postflight/host.json
[Install]
WantedBy=multi-user.target
"""
    files["postflight-dnsmasq.service"] = """[Unit]
Description=Postflight guest DHCP and DNS
Requires=postflight-network.service
After=postflight-network.service
Before=hostd.service
[Service]
ExecStart=/usr/sbin/dnsmasq --keep-in-foreground --conf-file=/etc/postflight/dnsmasq.conf --pid-file=/run/postflight-dnsmasq.pid
Restart=on-failure
RestartSec=2s
[Install]
WantedBy=multi-user.target
"""
    files["hostd-override.conf"] = """[Unit]
Requires=postflight-storage.service postflight-network.service postflight-dnsmasq.service
After=postflight-storage.service postflight-network.service postflight-dnsmasq.service
[Service]
EnvironmentFile=/etc/postflight/secrets.env
UMask=0027
"""
    files["postflight-reconcile.service"] = f"""[Unit]
Description=Converge Postflight host from protected Guardian main
Wants=network-online.target
After=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/local/libexec/postflight-host/reconcile.sh {m['host_id']}
TimeoutStartSec=10h
UMask=0027
"""
    files["postflight-reconcile.timer"] = """[Unit]
Description=Follow reviewed Postflight host configuration
[Timer]
OnBootSec=2min
OnUnitInactiveSec=5min
RandomizedDelaySec=30s
[Install]
WantedBy=timers.target
"""
    files["postflight.nft"] = nft_rules(m)
    return files


def command(*args, check=True, capture=True, **kwargs):
    return subprocess.run(args, check=check, text=True, capture_output=capture, **kwargs)


def secure_directory(path, mode=0o755):
    path = Path(path)
    if not path.exists():
        path.mkdir(mode=mode, parents=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError(f"directory must be root-owned and not writable by others: {path}")
    return path


def secret_file(path, length=None):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o077:
        raise ValueError(f"secret must be a root-owned private regular file: {path}")
    if length is not None and info.st_size != length:
        raise ValueError(f"{path} must contain exactly {length} bytes")


def write_file(path, data, mode=0o644):
    path = Path(path)
    secure_directory(path.parent)
    if path.is_symlink():
        raise ValueError(f"refusing symlink destination: {path}")
    data = data.encode() if isinstance(data, str) else data
    if path.exists() and path.read_bytes() == data and stat.S_IMODE(path.stat().st_mode) == mode:
        return False
    fd, temporary = tempfile.mkstemp(prefix=".postflight-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(data)
            os.fchmod(output.fileno(), mode)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return True


def ensure_storage(m):
    secure_directory(CONFIG, 0o700)
    secret_file(CONFIG / "zfs.key", 32)
    backing = Path(m["vdev_file"])
    secure_directory(backing.parent, 0o700)
    imported = command("zpool", "list", "-H", m["pool"], check=False).returncode == 0
    if not imported and backing.exists():
        info = backing.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o077:
            raise ValueError("existing vdev must be a root-owned private regular file")
        # Existing bytes are never truncated or reformatted, even if import
        # fails. This includes interrupted creates and an unexpected pool label.
        command("zpool", "import", "-d", str(backing.parent), m["pool"])
    elif not imported:
        fd = os.open(backing, os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            os.posix_fallocate(fd, 0, m["vdev_gib"] << 30)
        finally:
            os.close(fd)
        command("zpool", "create", "-o", "ashift=12", "-O", "mountpoint=none", m["pool"], str(backing))
    status = command("zpool", "status", "-P", m["pool"]).stdout
    if str(backing) not in status.split():
        raise ValueError("imported pool does not use the declared backing file")
    dataset = m["pool"] + "/postflight"
    if command("zfs", "list", "-H", dataset, check=False).returncode:
        command("zfs", "create", "-o", "encryption=aes-256-gcm", "-o", "keyformat=raw",
                "-o", "keylocation=file:///etc/postflight/zfs.key", "-o", "mountpoint=/var/lib/postflight",
                "-o", "compression=lz4", "-o", "atime=off", dataset)
    for property_name, expected in (("encryption", "aes-256-gcm"), ("keyformat", "raw"),
                                    ("keylocation", "file:///etc/postflight/zfs.key"),
                                    ("mountpoint", "/var/lib/postflight")):
        if command("zfs", "get", "-H", "-o", "value", property_name, dataset).stdout.strip() != expected:
            raise ValueError(f"existing dataset {property_name} differs from desired storage contract")
    if command("zfs", "get", "-H", "-o", "value", "keystatus", dataset).stdout.strip() != "available":
        command("zfs", "load-key", dataset)
    if command("zfs", "get", "-H", "-o", "value", "mounted", dataset).stdout.strip() != "yes":
        command("zfs", "mount", dataset)
    secure_directory("/var/lib/postflight")
    os.chmod("/var/lib/postflight", 0o751)


def ensure_network(m):
    link = command("ip", "-j", "-d", "link", "show", "dev", m["bridge"], check=False)
    if link.returncode:
        command("ip", "link", "add", "name", m["bridge"], "type", "bridge")
    elif json.loads(link.stdout)[0].get("linkinfo", {}).get("info_kind") != "bridge":
        raise ValueError("declared guest bridge is an unrelated interface")
    command("ip", "address", "replace", m["gateway"] + "/24", "dev", m["bridge"])
    command("ip", "link", "set", "dev", m["bridge"], "up")
    command("sysctl", "-w", "net.ipv4.ip_forward=1")
    command("sysctl", "-w", f"net.ipv6.conf.{m['bridge']}.disable_ipv6=1")
    existing = [family for family in ("inet", "bridge")
                if command("nft", "list", "table", family, "postflight_ci", check=False).returncode == 0]
    rules = nft_rules(m, existing)
    command("nft", "--check", "-f", "-", input=rules)
    command("nft", "-f", "-", input=rules)
    # An ACCEPT in our base chain cannot override UFW's later DROP. Install
    # the complete deny boundary before adding the scoped UFW exceptions.
    ensure_ufw(m)


def install(args, m):
    for executable in ("zpool", "zfs", "ip", "bridge", "nft", "sysctl", "dnsmasq", "systemctl", "git", "flock"):
        if not shutil.which(executable):
            raise ValueError(f"missing provisioned prerequisite {executable}; no unpinned package installation is attempted")
    if command(m["qemu_path"], "--version").stdout.splitlines()[0] != m["qemu_version"]:
        raise ValueError("QEMU version differs from exact host manifest pin")
    if not Path(m["firmware"]).is_file():
        raise ValueError("pinned guest firmware path is missing")
    secure_directory(CONFIG, 0o700)
    secret_file(CONFIG / "secrets.env")
    ensure_storage(m)
    if not (CONFIG / "host.key").exists():
        write_file(CONFIG / "host.key", os.urandom(64), 0o600)
    secret_file(CONFIG / "host.key", 64)
    try:
        user = pwd.getpwnam("postflight-vm")
        if user.pw_shell not in ("/usr/sbin/nologin", "/sbin/nologin", "/bin/false") or user.pw_uid == 0:
            raise ValueError("postflight-vm must be an unprivileged non-login account")
    except KeyError:
        command("useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent",
                "--shell", "/usr/sbin/nologin", "postflight-vm")
    command("usermod", "--append", "--groups", "kvm", "postflight-vm")
    image = m["pool"] + "/postflight/images/" + args.image_id + "@golden"
    command("zfs", "list", "-H", "-t", "snapshot", image)
    files = render(m, args.image_id, args.criu_version)
    source = Path(__file__).resolve().parent
    repo = source.parents[2]
    host_changed = write_file("/usr/local/bin/hostd", Path(args.hostd).read_bytes(), 0o755)
    host_changed |= write_file("/usr/local/libexec/postflight-tap",
                               (repo / "src/postflight/hostd/cmd/hostd/postflight-tap.sh").read_bytes(), 0o755)
    host_changed |= write_file("/etc/systemd/system/hostd.service",
                               (repo / "src/postflight/hostd/cmd/hostd/hostd.service").read_bytes())
    config_changed = write_file(CONFIG / "host.json", json.dumps(m, indent=2) + "\n", 0o600)
    network_changed = config_changed
    for name, content in files.items():
        if name == "hostd-override.conf":
            changed = write_file("/etc/systemd/system/hostd.service.d/postflight.conf", content)
            host_changed |= changed
        elif name.endswith((".service", ".timer")):
            changed = write_file(Path("/etc/systemd/system") / name, content)
            host_changed |= changed
        else:
            changed = write_file(CONFIG / name, content, 0o600)
            host_changed |= changed
            network_changed |= changed and name in ("dnsmasq.conf", "postflight.nft")
    for name in ("host.py", "reconcile.sh"):
        write_file(LIBEXEC / name, (source / name).read_bytes(), 0o755)
    write_file("/etc/sysctl.d/90-postflight.conf", "net.ipv4.ip_forward=1\n")
    # A change to the externally supplied bearer reloads hostd without ever
    # storing the bearer or its hash in a receipt.
    secret_revision = str((CONFIG / "secrets.env").stat().st_mtime_ns) + "\n"
    host_changed |= write_file(CONFIG / "secrets.revision", secret_revision, 0o600)
    command("dnsmasq", "--test", "--conf-file=" + str(CONFIG / "dnsmasq.conf"))
    command("systemctl", "daemon-reload")
    command("systemctl", "enable", "postflight-storage.service", "postflight-network.service", "postflight-dnsmasq.service", "hostd.service")
    command("systemctl", "start", "postflight-storage.service")
    command("systemctl", "restart" if network_changed else "start", "postflight-network.service", "postflight-dnsmasq.service")
    command("systemctl", "restart" if host_changed else "start", "hostd.service")
    if args.enable_reconcile:
        command("systemctl", "enable", "--now", "postflight-reconcile.timer")
    print(f"Postflight {m['host_id']} configured with {args.image_id}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("render", "storage", "network", "install"))
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--image-id")
    parser.add_argument("--criu-version", default="4.2")
    parser.add_argument("--hostd")
    parser.add_argument("--output")
    parser.add_argument("--enable-reconcile", action="store_true")
    args = parser.parse_args()
    m = load_manifest(args.manifest)
    if args.operation == "render":
        if not args.output or not args.image_id:
            parser.error("render requires --output and --image-id")
        output = Path(args.output)
        output.mkdir(parents=True, exist_ok=True)
        for name, content in render(m, args.image_id, args.criu_version).items():
            (output / name).write_text(content)
        return
    if os.geteuid() != 0 or sys.platform != "linux":
        parser.error("mutations require root on the Linux runner host")
    if args.operation == "storage":
        ensure_storage(m)
    elif args.operation == "network":
        ensure_network(m)
    else:
        if not args.hostd or not args.image_id:
            parser.error("install requires --hostd and --image-id")
        install(args, m)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"postflight-host: {error}", file=sys.stderr)
        sys.exit(1)
