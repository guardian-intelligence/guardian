"""Select first-party images from changed build inputs for the main publisher."""

import json
import os
from pathlib import Path
import re
import subprocess

WATCH = {
    'alert-relay': '^src/alerting/',
    'analytics-ingest': '^src/analytics/|^src/proto/',
    'authorization': '^src/authorization/|^src/proto/guardian/authorization/|^src/proto/gen/go/guardian/authorization/',
    'chunkies-gateway': '^src/chunkies/|^src/games/',
    'chunkies-chunkie': '^src/chunkies/|^src/games/',
    'payments': '^src/payments/',
    'payments-canary': '^src/payments/',
    'postflight-controlplane': '^src/postflight/(controlplane|canaryloop|hostd/syncproto)/',
    'feature-flags-notify': '^src/flags/notify/',
    'tofu-runner': '^src/infrastructure/cmd/tofu_runner/',
    'company-site': '^src/company/web/|^src/shared/ts/(brand|telemetry)/',
    'privatecut-web': '^src/privatecut/web/|^src/shared/ts/(brand|telemetry)/',
    'wake-up-mythra-web': '^src/games/wake-up-mythra/|^src/chunkies/|^src/shared/ts/telemetry/',
    'anveio-cal': '^src/anveio/cal/',
    'anveio-web': '^src/anveio/web/|^src/shared/ts/brand/',
}

TARGETS = {
    'alert-relay': '//src/alerting/relay:push',
    'analytics-ingest': '//src/analytics/ingest:push',
    'authorization': '//src/authorization:push',
    'chunkies-gateway': '//src/chunkies:chunkies_gateway_push',
    'chunkies-chunkie': '//src/chunkies:chunkies_chunkie_push',
    'payments': '//src/payments:push',
    'payments-canary': '//src/payments/canary:push',
    'postflight-controlplane': '//src/postflight/controlplane:push',
    'feature-flags-notify': '//src/flags/notify:push',
    'tofu-runner': '//src/infrastructure/cmd/tofu_runner:push',
    'company-site': '//src/company/web/site:push',
    'privatecut-web': '//src/privatecut/web/site:push',
    'wake-up-mythra-web': '//src/games/wake-up-mythra/web/site:push',
    'anveio-cal': '//src/anveio/cal/site:push',
    'anveio-web': '//src/anveio/web/site:push',
}

WEB_IMAGES = {"company-site", "privatecut-web", "wake-up-mythra-web", "anveio-cal", "anveio-web"}
COMMON = re.compile(r"^(MODULE\.bazel(\.lock)?|\.bazelrc|\.bazelversion|\.github/workflows/images\.yml|\.github/scripts/classify_images\.py)$")
WEB_COMMON = re.compile(r"^(pnpm-(lock|workspace)\.yaml|package\.json|tsconfig(\.base)?\.json|vite\.config\.ts|\.npmrc)$|^patches/")


def classify(paths, all_images=False):
    selected = []
    for name, pattern in WATCH.items():
        def matches(path):
            return (COMMON.search(path) or re.search(pattern, path)
                    or (name in WEB_IMAGES and WEB_COMMON.search(path))
                    or (name not in WEB_IMAGES and path in {"go.mod", "go.sum"}))
        if all_images or any(matches(path) for path in paths):
            selected.append({"name": name, "target": TARGETS[name]})
    return selected


def changed_paths(event_name, event):
    if event_name != "push":
        return None
    base = event.get("before", "")
    if not re.fullmatch(r"[0-9a-f]{40}", base) or set(base) == {"0"}:
        return None
    if subprocess.run(["git", "cat-file", "-e", base], capture_output=True).returncode:
        return None
    return subprocess.check_output(["git", "diff", "--name-only", base, "HEAD"], text=True).splitlines()


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    paths = changed_paths(os.environ["GITHUB_EVENT_NAME"], event)
    images = json.dumps(classify(paths or [], all_images=paths is None), separators=(",", ":"))
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"images={images}\n")
    print(f"classified: images={images}")


if __name__ == "__main__":
    main()
