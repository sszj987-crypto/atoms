#!/bin/sh
set -eu
# Runs only as the isolated Runtime's unprivileged user. No platform cookies,
# credentials, user-provided URL, workspace writes, or persisted browser profile.
test "$(id -u)" != 0
capture_dir=$(mktemp -d /tmp/atoms-version-screen.XXXXXX)
trap 'rm -rf "$capture_dir"' EXIT HUP INT TERM
# Docker provides process/filesystem isolation; Chromium's nested sandbox is
# unavailable under the Runtime's default seccomp/user namespace constraints.
timeout 10 chromium-browser --headless --no-sandbox --disable-gpu \
  --disable-dev-shm-usage --hide-scrollbars --no-first-run --disable-background-networking \
  --user-data-dir="$capture_dir/profile" --window-size=1280,720 \
  --virtual-time-budget=3000 --screenshot="$capture_dir/home.png" \
  http://127.0.0.1:3000/ >"$capture_dir/browser.log" 2>&1
test -f "$capture_dir/home.png"
test "$(wc -c < "$capture_dir/home.png")" -le 3145728
# Keep lines short for the Docker exec stream's bounded line scanner.
base64 "$capture_dir/home.png"
