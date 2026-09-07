#!/usr/bin/env python3
"""Bind golden-image reuse to guest inputs and an immutable ZFS snapshot GUID."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile


# These are executable recipes, not all files in their directories. Host code,
# documentation, test-only edits, and HEAD itself do not alter guest contents.
RECIPE_PATHS = (
    "src/postflight/image/build.sh",
    "src/postflight/image/build-upstream.sh",
    "src/postflight/image/render-qemu-template.py",
    "src/postflight/image/image_identity.py",
    "src/postflight/image/pins.env",
    "src/postflight/runner/build.sh",
    "src/postflight/runner/runner-listener.patch",
)


def digest_file(path):
    if not path.is_file():
        raise ValueError("missing image input: " + str(path))
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def image_identity(repo, guestd, listener, base, flavor):
    if flavor not in ("turbo", "confidential"):
        raise ValueError("unsupported image flavor")
    inputs = {
        "format": "postflight-guest-inputs-v1",
        "flavor": flavor,
        "recipes": {name: {"sha256": digest_file(repo / name),
                           "mode": stat.S_IMODE((repo / name).stat().st_mode)} for name in RECIPE_PATHS},
        "artifacts": {name: digest_file(path) for name, path in
                      (("guestd", guestd), ("runner_listener", listener), ("upstream_qcow2", base))},
    }
    encoded = json.dumps(inputs, sort_keys=True, separators=(",", ":")).encode()
    digest = hashlib.sha256(encoded).hexdigest()
    commit = subprocess.run(["git", "-C", str(repo), "rev-parse", "HEAD"],
                            check=True, text=True, capture_output=True).stdout.strip()
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("invalid source commit")
    # File bytes above bind dirty and untracked recipe inputs directly. Keep
    # their source status as provenance without making unrelated HEAD churn
    # part of an immutable guest image's cache identity.
    dirty = subprocess.run(["git", "-C", str(repo), "status", "--porcelain=v1", "--untracked-files=all", "--", *RECIPE_PATHS],
                           check=True, text=True, capture_output=True).stdout.splitlines()
    return {"schema": 1, "image_id": f"noble-{flavor}-{digest}", "input_sha256": digest,
            "inputs": inputs, "source": {"commit": commit, "dirty_recipes": dirty}}


def private_json(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or info.st_size > 1 << 20:
        raise ValueError("image receipt must be a private owned regular file")
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise ValueError("invalid image receipt")
    return value


def snapshot_guid(snapshot):
    result = subprocess.run(["zfs", "get", "-H", "-p", "-o", "value", "guid", snapshot],
                            check=True, text=True, capture_output=True)
    guid = result.stdout.strip()
    if not re.fullmatch(r"[1-9][0-9]{0,19}", guid) or int(guid) > 2**64 - 1:
        raise ValueError("invalid golden snapshot GUID")
    return guid


def verify_receipt(expected, receipt, snapshot, guid):
    for key in ("schema", "image_id", "input_sha256", "inputs"):
        if receipt.get(key) != expected[key]:
            raise ValueError("golden image receipt does not bind the requested inputs")
    if receipt.get("snapshot") != snapshot or receipt.get("snapshot_guid") != guid:
        raise ValueError("golden image snapshot was replaced or is not bound to its receipt")
    source = receipt.get("source")
    if not isinstance(source, dict) or not re.fullmatch(r"[0-9a-f]{40}", source.get("commit", "")):
        raise ValueError("golden image receipt has no source provenance")


def publish_receipt(expected, path, snapshot):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.parent.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise ValueError("untrusted image receipt directory")
    receipt = dict(expected, snapshot=snapshot, snapshot_guid=snapshot_guid(snapshot))
    fd, temporary = tempfile.mkstemp(prefix=".image-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(receipt, output, sort_keys=True, indent=2)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "verify", "publish"))
    parser.add_argument("--identity", type=Path, required=True)
    parser.add_argument("--repo", type=Path)
    parser.add_argument("--guestd", type=Path)
    parser.add_argument("--listener", type=Path)
    parser.add_argument("--base", type=Path)
    parser.add_argument("--flavor")
    parser.add_argument("--receipt", type=Path)
    parser.add_argument("--snapshot")
    args = parser.parse_args()
    if args.operation == "prepare":
        if any(value is None for value in (args.repo, args.guestd, args.listener, args.base, args.flavor)):
            parser.error("prepare requires repo, guestd, listener, base, and flavor")
        identity = image_identity(args.repo, args.guestd, args.listener, args.base, args.flavor)
        args.identity.write_text(json.dumps(identity, sort_keys=True) + "\n")
        os.chmod(args.identity, 0o600)
        print(identity["image_id"])
    else:
        if args.receipt is None or args.snapshot is None:
            parser.error("verify/publish requires receipt and snapshot")
        identity = private_json(args.identity)
        if args.operation == "verify":
            verify_receipt(identity, private_json(args.receipt), args.snapshot, snapshot_guid(args.snapshot))
        else:
            publish_receipt(identity, args.receipt, args.snapshot)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"image-identity: {error}")
