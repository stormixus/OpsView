#!/usr/bin/env python3
"""ROI-based Korean plate reader for a FIXED camera (e.g. the mechanical-parking
ramp, dvr3_ch1). The plate of an entering/leaving car always appears in the same
rectangle, and the DVR OSD is elsewhere — so we just crop that rectangle, upscale
it, and run EasyOCR's Korean recogniser on it. No car/plate detector, no OSD noise.

Usage (in the EasyKoreanLpDetector repo dir, which holds lp_models/):
    # 1) first see a sample frame + its size:
    python batch.py /frames
    # 2) open /frames/sample.jpg, read off the plate rectangle, rerun:
    ROI=x1,y1,x2,y2 python batch.py /frames        # pixels
    ROI=0.45,0.5,0.95,0.85 python batch.py /frames  # or fractions 0..1
Tune with UPSCALE (default 3). Outputs: lpr_results.csv, roi_preview.jpg, crops/*_roi.jpg
"""
import csv
import glob
import os
import re
import sys

import cv2
import easyocr
import numpy as np
import torch
from PIL import Image

_HANGUL = re.compile(r"[가-힣]")


def is_korean_plate(text):
    """A Korean plate has EXACTLY one Hangul syllable and 4-7 digits, length 6-9
    (12가3456 / 123가4567 / 서울12가3456). Exactly-one-Hangul rejects multi-Hangul
    OCR garbage ('8소소소995') as well as OSD 'Camera'/'01'/dates."""
    t = re.sub(r"\s", "", text or "")
    digits = sum(c.isdigit() for c in t)
    return len(_HANGUL.findall(t)) == 1 and 4 <= digits <= 7 and 6 <= len(t) <= 9


def parse_roi(s, w, h):
    try:
        v = [float(x) for x in s.split(",")]
    except ValueError:
        return None
    if len(v) != 4:
        return None
    if max(v) <= 1.0:  # fractions of the frame
        v = [v[0] * w, v[1] * h, v[2] * w, v[3] * h]
    x1, y1, x2, y2 = (int(round(x)) for x in v)
    x1, x2 = max(0, min(x1, w)), max(0, min(x2, w))
    y1, y2 = max(0, min(y1, h)), max(0, min(y2, h))
    return (x1, y1, x2, y2) if (x2 > x1 and y2 > y1) else None


def read_roi(reader, arr, roi, upscale, crop_path):
    """Crop the ROI, upscale, and DETECT+read text inside it (readtext) — so the
    plate is found wherever it sits in the region (its position varies as the car
    drives in/out). Returns (best_plate, conf, raw_all_text)."""
    x1, y1, x2, y2 = roi
    sub = arr[y1:y2, x1:x2]
    if sub.size == 0:
        return ("", 0.0, "")
    if upscale and upscale != 1:
        sub = cv2.resize(sub, None, fx=upscale, fy=upscale, interpolation=cv2.INTER_CUBIC)
    cv2.imwrite(crop_path, cv2.cvtColor(sub, cv2.COLOR_RGB2BGR))
    results = reader.readtext(sub)  # [(box, text, conf)] anywhere in the ROI
    raw = " | ".join(t for (_, t, _) in results)
    plate = ("", 0.0)
    for (_, text, conf) in results:
        if is_korean_plate(text) and conf > plate[1]:
            plate = (text, float(conf))
    # NOTE: no whole-ROI recognize() fallback — it hallucinates a plate-shaped string
    # from any digit noise / signage, which inflated the count with garbage.
    return plate[0], plate[1], raw


def main():
    folder = sys.argv[1] if len(sys.argv) > 1 else "/frames"
    roi_str = os.environ.get("ROI", "").strip()
    upscale = float(os.environ.get("UPSCALE", "3") or 3)
    files = sorted(glob.glob(os.path.join(folder, "*.jpg")))
    if not files:
        print("no .jpg files in", folder)
        sys.exit(1)
    cropdir = os.path.join(folder, "crops")
    os.makedirs(cropdir, exist_ok=True)

    print("loading EasyOCR (best_acc Korean recogniser)...")
    reader = easyocr.Reader(
        ["en"], detect_network="craft", recog_network="best_acc",
        user_network_directory="lp_models/user_network",
        model_storage_directory="lp_models/models",
        gpu=torch.cuda.is_available(),
    )

    roi = None
    rows, read = [], 0
    for i, p in enumerate(files, 1):
        arr = np.array(Image.open(p).convert("RGB"))
        h, w = arr.shape[:2]
        base = os.path.splitext(os.path.basename(p))[0]
        if i == 1:
            cv2.imwrite(os.path.join(folder, "sample.jpg"), cv2.cvtColor(arr, cv2.COLOR_RGB2BGR))
            print(f"frame size: {w}x{h}  (saved sample.jpg)")
            if roi_str:
                roi = parse_roi(roi_str, w, h)
                if roi:
                    prev = arr.copy()
                    cv2.rectangle(prev, (roi[0], roi[1]), (roi[2], roi[3]), (255, 0, 0), 2)
                    cv2.imwrite(os.path.join(folder, "roi_preview.jpg"), cv2.cvtColor(prev, cv2.COLOR_RGB2BGR))
                    print(f"ROI = {roi}  (saved roi_preview.jpg — verify the box covers the plate)")
                else:
                    print("ROI string invalid, ignoring:", roi_str)
        if not roi:
            continue
        text, conf, raw = read_roi(reader, arr, roi, upscale, os.path.join(cropdir, base + "_roi.jpg"))
        if text:
            read += 1
            print(f"[{i}/{len(files)}] {base} -> {text}  ({conf:.2f})")
        rows.append([base, text, raw, round(conf, 3)])

    out_csv = os.path.join(folder, "lpr_results.csv")
    with open(out_csv, "w", newline="") as f:
        w_ = csv.writer(f)
        w_.writerow(["file", "plate", "raw_ocr", "conf"])
        w_.writerows(rows)

    if not roi:
        print("\nNo ROI set. Open sample.jpg, find the rectangle where the plate shows,")
        print("then rerun:   docker run ... -e ROI=x1,y1,x2,y2 ...   (or fractions 0..1)")
    else:
        print(f"\n== {len(files)} frames ==  KOREAN PLATE read: {read} ({100*read//max(len(files),1)}%)")
        print(f"  check roi_preview.jpg (is the box on the plate?) + crops/*_roi.jpg (legible?)")
        print(f"  -> {out_csv}")


if __name__ == "__main__":
    main()
