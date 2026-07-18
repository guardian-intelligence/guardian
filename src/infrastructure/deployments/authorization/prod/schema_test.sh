#!/usr/bin/env bash
set -euo pipefail

"$2" validate --fail-on-warn "$1"
