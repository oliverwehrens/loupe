#!/usr/bin/env bash
# Loupe end-to-end smoke test.
#
# Runs scripts/smoke (a Go program) which spins up an in-process httptest
# server emulating Bitbucket Cloud + Jira Cloud, runs `loupe baseline`
# and `loupe status` against it, and asserts the resulting deck.
#
# Usage:  ./scripts/smoke.sh

set -euo pipefail

cd "$(dirname "$0")/.."
go run ./scripts/smoke
