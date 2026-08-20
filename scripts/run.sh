#!/usr/bin/env bash
# Build and run the antibot scan, then publish the report to the GitHub
# Actions step summary. The report is also stored as data/reports/<date>.md.
set -euo pipefail
cd "$(dirname "$0")/.."

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

go run ./src -config config.json >"$tmp"

# Refresh the keep-alive marker so GitHub Actions keeps the schedule alive.
uuidgen > .keep_alive

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  cat "$tmp" >>"$GITHUB_STEP_SUMMARY"
fi