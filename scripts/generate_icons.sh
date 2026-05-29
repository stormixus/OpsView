#!/usr/bin/env bash
# Regenerate raster icons from SVG sources. Requires: python venv at repo root (.venv-icons).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ -d "${ROOT}/.venv-icons/Scripts" ]]; then
  PY="${ROOT}/.venv-icons/Scripts/python.exe"
else
  PY="${ROOT}/.venv-icons/bin/python"
fi

if [[ ! -f "$PY" ]]; then
  echo "Error: Python executable not found at $PY"
  exit 1
fi

GEN="${ROOT}/generate_ico.py"

echo "Generating tray.ico (agent + relay)..."
"$PY" "$GEN" "${ROOT}/icon-dark.svg" "${ROOT}/agent/tray.ico"
"$PY" "$GEN" "${ROOT}/icon-dark.svg" "${ROOT}/relay/tray.ico"

mkdir -p "${ROOT}/viewer/build"
echo "Generating viewer appicon (appicon.png + build/appicon.png)..."
"$PY" -c "
import cairosvg, shutil
out = '${ROOT}/viewer/appicon.png'
# Full-bleed PNG (no transparent corners). icon-dark.svg == icon-dark-apple.svg
cairosvg.svg2png(url='${ROOT}/icon-dark.svg', write_to=out, output_width=1024, output_height=1024)
shutil.copy(out, '${ROOT}/viewer/build/appicon.png')
"

echo "Done."
