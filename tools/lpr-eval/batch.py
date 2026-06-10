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
import re
import sys

import cv2
import easyocr
import numpy as np
import torch
from PIL import Image

PLATE_SIZE = (224, 128)  # (w, h) — matches server.py
_HANGUL = re.compile(r"[가-힣]")


def is_korean_plate(text):
    """Reject OSD junk ('01', 'Camera', date/time): a Korean plate is ~7-8 chars
    with exactly one Hangul syllable and 4-6 digits (e.g. 12가3456 / 123가4567)."""
    t = re.sub(r"\s", "", text)
    digits = sum(c.isdigit() for c in t)
    return bool(_HANGUL.search(t)) and 4 <= digits <= 7 and 6 <= len(t) <= 9


VEHICLE_CLS = {2, 3, 5, 7}  # COCO: car, motorcycle, bus, truck


def load_models():
    from ultralytics import YOLO
    car_m = YOLO("yolo26s.pt")
    lp_m = torch.hub.load("ultralytics/yolov5", "custom", "lp_det.pt", trust_repo=True)
    reader = easyocr.Reader(
        ["en"],
        detect_network="craft",
        recog_network="best_acc",
        user_network_directory="lp_models/user_network",
        model_storage_directory="lp_models/models",
        gpu=torch.cuda.is_available(),
    )
    return car_m, lp_m, reader


def read_plates(car_m, lp_m, reader, path, cropdir):
    """Return (n_cars, n_plate_boxes, [(text, conf)]). Mirrors server.py: detect
    VEHICLES first, then look for a plate ONLY inside each car box — so signs/banners
    in the scene can't be mis-read as plates. Saves each plate crop to cropdir."""
    im = Image.open(path).convert("RGB")
    arr = np.array(im)
    base = os.path.splitext(os.path.basename(path))[0]

    cars = []
    for r in car_m(arr, verbose=False):
        for bx in r.boxes:
            if int(bx.cls[0]) in VEHICLE_CLS:
                cars.append([int(v) for v in bx.xyxy[0].tolist()])

    out, nplate = [], 0
    for ci, (cx1, cy1, cx2, cy2) in enumerate(cars):
        if cx2 - cx1 < 16 or cy2 - cy1 < 16:
            continue
        car_pil = Image.fromarray(arr[cy1:cy2, cx1:cx2])
        for b in lp_m(car_pil).xyxy[0].tolist():
            ax, ay, bx2, by2 = int(b[0]), int(b[1]), int(b[2]), int(b[3])
            crop = arr[cy1 + ay:cy1 + by2, cx1 + ax:cx1 + bx2]
            if crop.size == 0:
                continue
            nplate += 1
            cv2.imwrite(os.path.join(cropdir, f"{base}_c{ci}_{nplate}.jpg"), cv2.cvtColor(crop, cv2.COLOR_RGB2BGR))
            gray = cv2.cvtColor(cv2.resize(crop, PLATE_SIZE), cv2.COLOR_BGR2GRAY)
            res = reader.recognize(gray)
            if res and res[0][1]:
                out.append((res[0][1], float(res[0][2])))
    return len(cars), nplate, out


def main():
    folder = sys.argv[1] if len(sys.argv) > 1 else "/frames"
    files = sorted(glob.glob(os.path.join(folder, "*.jpg")))
    if not files:
        print("no .jpg files in", folder)
        sys.exit(1)
    cropdir = os.path.join(folder, "crops")
    os.makedirs(cropdir, exist_ok=True)
    print("loading models (first run downloads yolov5 + easyocr weights)...")
    car_m, lp_m, reader = load_models()

    rows, with_car, with_plate, read = [], 0, 0, 0
    for i, p in enumerate(files, 1):
        try:
            ncar, nplate, plates = read_plates(car_m, lp_m, reader, p, cropdir)
        except Exception as e:  # keep going on a bad frame
            ncar, nplate, plates = 0, 0, []
            print("  !", os.path.basename(p), "error:", e)
        if ncar:
            with_car += 1
        if nplate:
            with_plate += 1
        # keep only outputs shaped like a Korean plate (drops OSD '01'/'Camera' etc.)
        plausible = [pl for pl in plates if is_korean_plate(pl[0])]
        best = max(plausible, key=lambda x: x[1]) if plausible else ("", 0.0)
        raw = (max(plates, key=lambda x: x[1])[0] if plates else "")
        if best[0]:
            read += 1
        rows.append([os.path.basename(p), ncar, nplate, best[0], raw, round(best[1], 3)])
        if best[0]:
            print(f"[{i}/{len(files)}] {os.path.basename(p)}  car={ncar} plate={nplate}  -> {best[0]}")

    out_csv = os.path.join(folder, "lpr_results.csv")
    with open(out_csv, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["file", "cars", "plate_boxes", "plate", "raw_ocr", "conf"])
        w.writerows(rows)
    n = len(files)
    pct = lambda x: 100 * x // max(n, 1)
    print(f"\n== {n} frames ==")
    print(f"  frames with a VEHICLE:        {with_car} ({pct(with_car)}%)")
    print(f"  frames with a PLATE box:      {with_plate} ({pct(with_plate)}%)")
    print(f"  frames with a KOREAN PLATE:   {read} ({pct(read)}%)   <- the real signal")
    print(f"  -> CSV: {out_csv}   crops: {cropdir}")
    print("'plate' col = plate-shaped only; 'raw_ocr' col = whatever OCR said (incl OSD junk).")
    print("If raw_ocr is full of '01'/'Camera'/dates, turn OFF the DVR OSD for a clean test.")


if __name__ == "__main__":
    main()
