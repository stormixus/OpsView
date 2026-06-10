# ROI Korean-plate reader for a fixed camera (e.g. dvr3_ch1 mechanical-parking ramp).
# Uses only EasyOCR's Korean recogniser (best_acc) on a cropped ROI — no YOLO.
# Build/run on the Unraid box (needs the Nvidia-Driver plugin for --gpus):
#
#   cd /mnt/user/stack/OpsView/tools/lpr-eval && docker build -t lpr-eval .
#   # 1) sample frame:
#   docker run --rm --gpus all -v /mnt/user/Records/SM-Boutique/dvr3_ch1/.evthumbs:/frames lpr-eval
#   # 2) open .../sample.jpg, read the plate rectangle, then:
#   docker run --rm --gpus all -e ROI=x1,y1,x2,y2 \
#     -v /mnt/user/Records/SM-Boutique/dvr3_ch1/.evthumbs:/frames lpr-eval
FROM pytorch/pytorch:2.3.1-cuda12.1-cudnn8-runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    git libgl1 libglib2.0-0 ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app
# the repo provides the EasyOCR custom Korean network under lp_models/
RUN git clone --depth 1 https://github.com/gyupro/EasyKoreanLpDetector.git
RUN pip install --no-cache-dir easyocr opencv-python-headless pillow numpy

COPY batch.py /app/batch.py
WORKDIR /app/EasyKoreanLpDetector
ENTRYPOINT ["python", "/app/batch.py"]
CMD ["/frames"]
