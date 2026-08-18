#!/usr/bin/env bash
# The installer the site serves at /postflight/install.sh is a copy of the
# crate's dist/install.sh: the company-site image bakes public/ in at build
# time, so the two only stay in step if a test says so.
set -euo pipefail

diff -u "$1" "$2"
