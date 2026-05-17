#!/usr/bin/env bash
# Regenerate the Loupe wordmark. Calls the sibling ../3dlogo tool with the
# exact parameters that produced the current logo, then rasterises to a
# 720×360 PNG via Inkscape and refreshes the deck-embedded copy.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
THREEDLOGO_DIR="${REPO_ROOT}/../3dlogo"
FONT="${HOME}/Fonts/BerkeleyMono-SemiBold-Condensed.otf"
GRADIENT='linear-gradient(180deg, #0f172a 0%, #1e40af 50%, #60a5fa 100%)'
SVG="${REPO_ROOT}/logo.svg"
PNG="${REPO_ROOT}/logo.png"
DECK_SVG="${REPO_ROOT}/internal/deck/assets/logo/loupe.svg"

[[ -d "${THREEDLOGO_DIR}" ]] || { echo "3dlogo not found at ${THREEDLOGO_DIR}" >&2; exit 1; }
[[ -f "${FONT}" ]]            || { echo "font not found at ${FONT}" >&2; exit 1; }

# Build the 3dlogo binary if missing.
if [[ ! -x "${THREEDLOGO_DIR}/3dlogo" ]]; then
  (cd "${THREEDLOGO_DIR}" && go build -o 3dlogo)
fi

(cd "${THREEDLOGO_DIR}" && ./3dlogo \
  -font "${FONT}" \
  -text loupe \
  -outline 8 \
  -flat-bottom=true \
  -gradient "${GRADIENT}" \
  -out "${SVG}")

# 720×360 PNG matches the previous raster dimensions so README links stay valid.
inkscape "${SVG}" -w 720 -h 360 -o "${PNG}"

# Keep the deck-embedded copy in sync.
cp "${SVG}" "${DECK_SVG}"

echo "wrote:"
ls -la "${SVG}" "${PNG}" "${DECK_SVG}"
