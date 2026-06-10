#!/usr/bin/env python3
"""Batch LPR evaluator for EasyKoreanLpDetector.

Runs the YOLOv5 plate detector + EasyOCR ("best_acc") Korean recogniser over every
JPEG in a folder and writes <folder>/lpr_results.csv with (file, plate, conf).
Mirrors server.py's inference calls but headless and over a whole folder, so you can
measure real accuracy on DVR frames without the Streamlit UI.

cwd must be the EasyKoreanLpDetector repo (the Dockerfile sets WORKDIR there) so the
relative lp_det.pt / lp_models/ paths resolve.

Usage:  python batch.py /frames
"""
import csv
import glob
import os
import sys

import cv2
import easyocr
import numpy as np
import torch
from PIL import Image

PLATE_SIZE = (224, 128)  # (w, h) — matches server.py


def load_models():
    lp_m = torch.hub.load("ultralytics/yolov5", "custom", "lp_det.pt", trust_repo=True)
    reader = easyocr.Reader(
        ["en"],
        detect_network="craft",
        recog_network="best_acc",
        user_network_directory="lp_models/user_network",
        model_storage_directory="lp_models/models",
        gpu=torch.cuda.is_available(),
    )
    return lp_m, reader


def read_plates(lp_m, reader, path):
    """Return [(text, conf)] for every plate box found in the image."""
    im = Image.open(path).convert("RGB")
    arr = np.array(im)
    out = []
    for b in lp_m(im).xyxy[0].tolist():  # [x1,y1,x2,y2,conf,cls]
        ax, ay, bx, by = int(b[0]), int(b[1]), int(b[2]), int(b[3])
        crop = arr[ay:by, ax:bx]
        if crop.size == 0:
            continue
        gray = cv2.cvtColor(cv2.resize(crop, PLATE_SIZE), cv2.COLOR_BGR2GRAY)
        res = reader.recognize(gray)
        if res:
            out.append((res[0][1], float(res[0][2])))
    return out


def main():
    folder = sys.argv[1] if len(sys.argv) > 1 else "/frames"
    files = sorted(glob.glob(os.path.join(folder, "*.jpg")))
    if not files:
        print("no .jpg files in", folder)
        sys.exit(1)
    print("loading models (first run downloads yolov5 + easyocr weights)...")
    lp_m, reader = load_models()

    rows, hit = [], 0
    for i, p in enumerate(files, 1):
        try:
            plates = read_plates(lp_m, reader, p)
        except Exception as e:  # keep going on a bad frame
            plates = []
            print("  !", os.path.basename(p), "error:", e)
        best = max(plates, key=lambda x: x[1]) if plates else ("", 0.0)
        if best[0]:
            hit += 1
        rows.append([os.path.basename(p), best[0], round(best[1], 3)])
        print(f"[{i}/{len(files)}] {os.path.basename(p)} -> {best[0] or '(none)'}")

    out_csv = os.path.join(folder, "lpr_results.csv")
    with open(out_csv, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["file", "plate", "conf"])
        w.writerows(rows)
    print(f"\ndone: {hit}/{len(files)} frames produced a plate string -> {out_csv}")
    print("(eyeball the CSV: how many plates are actually CORRECT, esp. ch1 night vs ch4 indoor)")


if __name__ == "__main__":
    main()
