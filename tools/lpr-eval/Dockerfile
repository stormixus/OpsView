# EasyKoreanLpDetector batch evaluator — runs a Korean LPR model over a folder of
# JPEGs and writes a CSV (file,plate,conf). For validating real DVR frames on
# Unraid + RTX 4080. Build/run on the Unraid box (needs the Nvidia-Driver plugin):
#
#   cd /mnt/user/stack/OpsView/tools/lpr-eval
#   docker build -t lpr-eval .
#   # point /frames at a channel's stored event snapshots (already on disk):
#   docker run --rm --gpus all \
#     -v /mnt/user/<recshare>/<agent>/dvr0_ch1/.evthumbs:/frames \
#     lpr-eval
#   # results -> /frames/lpr_results.csv
FROM pytorch/pytorch:2.3.1-cuda12.1-cudnn8-runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    git libgl1 libglib2.0-0 ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN git clone --depth 1 https://github.com/gyupro/EasyKoreanLpDetector.git

# EasyOCR + YOLOv5(torch.hub) runtime deps
RUN pip install --no-cache-dir \
    easyocr opencv-python-headless pillow numpy \
    pandas seaborn requests scipy tqdm pyyaml matplotlib

# warm the torch.hub yolov5 repo cache so runtime load is offline-fast
RUN python -c "import torch; torch.hub.load('ultralytics/yolov5','yolov5s', trust_repo=True)" || true

COPY batch.py /app/batch.py
WORKDIR /app/EasyKoreanLpDetector
ENTRYPOINT ["python", "/app/batch.py"]
CMD ["/frames"]
