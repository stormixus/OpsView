#!/usr/bin/env bash
# Regenerate raster icons from SVG sources. Requires: python venv at repo root (.venv-icons).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PY="${ROOT}/.venv-icons/bin/python"
GEN="${ROOT}/generate_ico.py"

if [[ ! -x "$PY" ]]; then
  echo "Run: python3 -m venv ${ROOT}/.venv-icons && ${ROOT}/.venv-icons/bin/pip install cairosvg pillow"
  exit 1
fi

echo "Generating tray.ico (agent + relay)..."
"$PY" "$GEN" "${ROOT}/icon-dark.svg" "${ROOT}/agent/tray.ico"
"$PY" "$GEN" "${ROOT}/icon-dark.svg" "${ROOT}/relay/tray.ico"

mkdir -p "${ROOT}/viewer/build"
echo "Generating viewer appicon (appicon.png + build/appicon.png)..."
"$PY" -c "
import cairosvg, shutil
out = '${ROOT}/viewer/appicon.png'
cairosvg.svg2png(url='${ROOT}/icon-dark-apple.svg', write_to=out, output_width=1024, output_height=1024)
shutil.copy(out, '${ROOT}/viewer/build/appicon.png')
"

echo "Done."
