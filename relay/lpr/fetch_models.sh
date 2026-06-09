#!/usr/bin/env bash
# Download default fast-alpr / open-image-models ONNX assets into ./models
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)/models"
mkdir -p "$DIR"

DET_URL="https://github.com/ankandrew/open-image-models/releases/download/assets/yolo-v9-t-384-license-plates-end2end.onnx"
OCR_URL="https://github.com/ankandrew/cnn-ocr-lp/releases/download/arg-plates/cct_s_v2_global.onnx"
CFG_URL="https://github.com/ankandrew/cnn-ocr-lp/releases/download/arg-plates/cct_s_v2_global_plate_config.yaml"

fetch() {
  local url="$1" out="$2"
  if [[ -f "$out" ]]; then
    echo "skip $out"
    return
  fi
  echo "fetch $out"
  curl -fL "$url" -o "$out"
}

fetch "$DET_URL" "$DIR/plate-detector.onnx"
fetch "$OCR_URL" "$DIR/plate-ocr.onnx"
fetch "$CFG_URL" "$DIR/plate-ocr.yaml"

cat <<EOF

Models saved under $DIR

Docker / Unraid (relay/docker-compose.yml):
  mkdir -p /mnt/user/appdata/opsview/lpr-models
  cp $DIR/* /mnt/user/appdata/opsview/lpr-models/
  # relay/.env:
  #   RELAY_LPR=1
  #   RELAY_LPR_MODELS_HOST=/mnt/user/appdata/opsview/lpr-models
  # docker compose pull && docker compose up -d

Local dev (macOS):
  export RELAY_LPR=1
  export RELAY_LPR_DETECTOR=$DIR/plate-detector.onnx
  export RELAY_LPR_OCR=$DIR/plate-ocr.onnx
  export RELAY_LPR_OCR_CONFIG=$DIR/plate-ocr.yaml
  export ORT_LIB_PATH=/opt/homebrew/lib/libonnxruntime.dylib
  cd relay && go build -tags onnx -o opsview-relay .

Note: global OCR models target Latin plates; swap in a Korea-trained OCR ONNX for 한국 번호판.
EOF
