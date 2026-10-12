#!/usr/bin/env python3
"""Store Mac transport credentials in Keychain; exec cloudflared with memory-only env."""

import ctypes
import json
import os
import sys
from datetime import datetime, timezone

SERVICE = b"org.guardianintelligence.mgmt-tunnel"
ACCOUNT = b"guardian-operator-shovon-mac"
ERR_NOT_FOUND = -25300


def keychain():
    security = ctypes.CDLL("/System/Library/Frameworks/Security.framework/Security")
    core = ctypes.CDLL("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
    uint = ctypes.c_uint32
    ptr = ctypes.c_void_p
    security.SecKeychainFindGenericPassword.argtypes = [
        ptr, uint, ctypes.c_char_p, uint, ctypes.c_char_p,
        ctypes.POINTER(uint), ctypes.POINTER(ptr), ctypes.POINTER(ptr),
    ]
    security.SecKeychainAddGenericPassword.argtypes = [
        ptr, uint, ctypes.c_char_p, uint, ctypes.c_char_p,
        uint, ctypes.c_char_p, ctypes.POINTER(ptr),
    ]
    security.SecKeychainItemModifyAttributesAndData.argtypes = [
        ptr, ptr, uint, ctypes.c_char_p,
    ]
    security.SecKeychainItemFreeContent.argtypes = [ptr, ptr]
    core.CFRelease.argtypes = [ptr]
    core.CFRelease.restype = None
    return security, core


def check(status):
    if status:
        raise RuntimeError(f"Keychain operation failed (OSStatus {status})")


def validate(credential):
    if not isinstance(credential, dict) or any(
        not isinstance(credential.get(key), str) or not credential[key]
        for key in ("client_id", "client_secret", "expires_at")
    ):
        raise ValueError("Credential must carry client_id, client_secret and expires_at")
    expires = datetime.fromisoformat(credential["expires_at"].replace("Z", "+00:00"))
    if expires.tzinfo is None:
        raise ValueError("Transport credential expiry must carry a timezone")
    if expires <= datetime.now(timezone.utc):
        raise ValueError("Transport credential has expired")
    return credential


def load():
    security, core = keychain()
    length = ctypes.c_uint32()
    data = ctypes.c_void_p()
    item = ctypes.c_void_p()
    status = security.SecKeychainFindGenericPassword(
        None, len(SERVICE), SERVICE, len(ACCOUNT), ACCOUNT,
        ctypes.byref(length), ctypes.byref(data), ctypes.byref(item),
    )
    if status == ERR_NOT_FOUND:
        return None
    check(status)
    try:
        return validate(json.loads(ctypes.string_at(data, length.value)))
    finally:
        security.SecKeychainItemFreeContent(None, data)
        core.CFRelease(item)


def store(credential):
    payload = json.dumps(validate(credential)).encode()
    security, core = keychain()
    item = ctypes.c_void_p()
    status = security.SecKeychainFindGenericPassword(
        None, len(SERVICE), SERVICE, len(ACCOUNT), ACCOUNT,
        None, None, ctypes.byref(item),
    )
    if status == ERR_NOT_FOUND:
        check(security.SecKeychainAddGenericPassword(
            None, len(SERVICE), SERVICE, len(ACCOUNT), ACCOUNT,
            len(payload), payload, ctypes.byref(item),
        ))
    else:
        check(status)
        try:
            check(security.SecKeychainItemModifyAttributesAndData(item, None, len(payload), payload))
        finally:
            core.CFRelease(item)
        return
    core.CFRelease(item)


def main():
    if sys.platform != "darwin":
        raise RuntimeError("Mac Keychain transport credentials require macOS")
    action = sys.argv[1] if len(sys.argv) > 1 else "status"
    if action == "store":
        store(json.load(sys.stdin))
        print("Operator Mac transport credential stored in login Keychain")
    elif action == "status":
        credential = load()
        print("Operator Mac transport credential expires " + credential["expires_at"]
              if credential else "No Mac transport credential; using the operator browser login")
    elif action == "run" and len(sys.argv) == 5:
        binary, hostname, port = sys.argv[2:]
        routes = {"k8s-operator.guardianintelligence.org": "16443",
                  "talos-operator.guardianintelligence.org": "50000"}
        if routes.get(hostname) != port:
            raise ValueError("Only declared operator tunnel routes are admitted")
        credential = load()
        env = dict(os.environ)
        env.pop("TUNNEL_SERVICE_TOKEN_ID", None)
        env.pop("TUNNEL_SERVICE_TOKEN_SECRET", None)
        if credential:
            env["TUNNEL_SERVICE_TOKEN_ID"] = credential["client_id"]
            env["TUNNEL_SERVICE_TOKEN_SECRET"] = credential["client_secret"]
        os.execve(binary, [binary, "access", "tcp", "--hostname", hostname,
                          "--url", "127.0.0.1:" + port], env)
    else:
        raise ValueError("Usage: mgmt_tunnel_credential.py store|status|run <binary> <hostname> <port>")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError):
        # Credential contents must never escape through logs or tracebacks.
        print("ERROR: Mac tunnel credential unavailable or invalid; check Keychain and expiry", file=sys.stderr)
        sys.exit(1)
