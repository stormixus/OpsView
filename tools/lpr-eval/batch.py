#!/usr/bin/env python3
"""ROI Korean-plate reader for the fixed mechanical-parking camera (dvr3_ch1).

Two input modes:
  * default — reads the small .evthumb snapshots in /frames (fast, but downscaled,
    so plates are tiny / unreadable).
  * REC mode (set -e REC=/rec to the channel's recording .mp4 dir) — for each event
    time it extracts the FULL-RES frame from the recording with ffmpeg, crops the
    ROI, and reads that. Much bigger plate pixels. RECOMMENDED.

ROI = the rectangle where the plate shows, as FRACTIONS 0..1 (OSD/signage outside).

Run (REC mode):
  docker run --rm --gpus all -e ROI=0.06,0.50,0.30,0.80 -e UPSCALE=2 -e REC=/rec \
    -v /mnt/user/Records/SM-Boutique/dvr3_ch1/.evthumbs:/frames \
    -v /mnt/user/Records/SM-Boutique/dvr3_ch1:/rec  lpr-eval
"""
import bisect
import csv
import datetime
import glob
import os
import re
import subprocess
import sys
from zoneinfo import ZoneInfo

import cv2
import easyocr
import numpy as np
import torch

_HANGUL = re.compile(r"[가-힣]")
# segment filenames are LOCAL wall-clock; attach the zone explicitly so unix
# conversion is correct regardless of the container's system localtime.
_TZ = ZoneInfo(os.environ.get("TZ", "Asia/Seoul"))


def is_korean_plate(text):
    """Exactly one Hangul syllable + 4-7 digits, length 6-9 (12가3456 / 123가4567)."""
    t = re.sub(r"\s", "", text or "")
    digits = sum(c.isdigit() for c in t)
    return len(_HANGUL.findall(t)) == 1 and 4 <= digits <= 7 and 6 <= len(t) <= 9


def parse_roi_frac(s):
    """ROI as fractions 0..1: 'x1,y1,x2,y2'. (REC mode crops with ffmpeg, needs fractions.)"""
    try:
        v = [float(x) for x in s.split(",")]
    except ValueError:
        return None
    if len(v) != 4 or max(v) > 1.0 or min(v) < 0.0:
        return None
    x1, y1, x2, y2 = v
    return (x1, y1, x2, y2) if (x2 > x1 and y2 > y1) else None


def seg_start_unix(path):
    # recorder names segments '<YYYYMMDD_HHMMSS>.mp4' in LOCAL wall-clock time
    dt = datetime.datetime.strptime(os.path.basename(path)[:15], "%Y%m%d_%H%M%S")
    return dt.replace(tzinfo=_TZ).timestamp()


def extract_rec_roi(segs, starts, t, roi, upscale, out):
    """ffmpeg: full-res frame from the recording at unix-time t, cropped to ROI."""
    i = bisect.bisect_right(starts, t) - 1
    if i < 0:
        return False
    off = t - starts[i]
    if off < 0 or off > 3600:  # t not inside this segment
        return False
    x1, y1, x2, y2 = roi
    vf = "crop=in_w*%.4f:in_h*%.4f:in_w*%.4f:in_h*%.4f" % (x2 - x1, y2 - y1, x1, y1)
    if upscale and upscale != 1:
        vf += ":flags=lanczos,scale=iw*%g:ih*%g:flags=lanczos" % (upscale, upscale)
    subprocess.run(
        ["ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", str(off), "-i", segs[i],
         "-frames:v", "1", "-vf", vf, "-q:v", "3", "-y", out],
        capture_output=True,
    )
    return os.path.exists(out) and os.path.getsize(out) > 0


def best_plate(reader, img_bgr):
    raw, plate = [], ("", 0.0)
    for (_, text, conf) in reader.readtext(img_bgr):
        raw.append(text)
        if is_korean_plate(text) and conf > plate[1]:
            plate = (text, float(conf))
    return plate[0], plate[1], " | ".join(raw)


def main():
    folder = sys.argv[1] if len(sys.argv) > 1 else "/frames"
    rec = os.environ.get("REC", "").strip()
    roi = parse_roi_frac(os.environ.get("ROI", "").strip())
    upscale = float(os.environ.get("UPSCALE", "2") or 2)
    files = sorted(glob.glob(os.path.join(folder, "*.jpg")))
    if not files:
        print("no .jpg files in", folder)
        sys.exit(1)
    cropdir = os.path.join(folder, "crops")
    os.makedirs(cropdir, exist_ok=True)

    if not roi:
        # save a sample so the user can pick the ROI fractions
        import shutil
        shutil.copy(files[0], os.path.join(folder, "sample.jpg"))
        print("No valid ROI (fractions 0..1). Saved sample.jpg — pick the plate rectangle,")
        print("then rerun with -e ROI=x1,y1,x2,y2 (fractions). For full-res add -e REC=/rec.")
        sys.exit(0)

    print("loading EasyOCR (best_acc Korean recogniser)...")
    reader = easyocr.Reader(
        ["en"], detect_network="craft", recog_network="best_acc",
        user_network_directory="lp_models/user_network",
        model_storage_directory="lp_models/models",
        gpu=torch.cuda.is_available(),
    )

    segs = starts = None
    if rec:
        pairs = sorted((seg_start_unix(p), p) for p in glob.glob(os.path.join(rec, "*.mp4")))
        starts = [s for s, _ in pairs]
        segs = [p for _, p in pairs]
        print(f"REC mode: {len(segs)} recording segments; extracting full-res frames.")

    rows, read, got = [], 0, 0
    for i, p in enumerate(files, 1):
        base = os.path.splitext(os.path.basename(p))[0]
        crop = os.path.join(cropdir, base + ("_rec.jpg" if rec else "_roi.jpg"))
        if rec and segs:
            try:
                t = int(base)  # evthumb filename = unix seconds
            except ValueError:
                continue
            if not extract_rec_roi(segs, starts, t, roi, upscale, crop):
                continue
            img = cv2.imread(crop)
        else:
            full = cv2.imread(p)
            h, w = full.shape[:2]
            x1, y1, x2, y2 = (int(roi[0] * w), int(roi[1] * h), int(roi[2] * w), int(roi[3] * h))
            img = full[y1:y2, x1:x2]
            if upscale and upscale != 1 and img.size:
                img = cv2.resize(img, None, fx=upscale, fy=upscale, interpolation=cv2.INTER_CUBIC)
            if img.size:
                cv2.imwrite(crop, img)
        if img is None or img.size == 0:
            continue
        got += 1
        text, conf, raw = best_plate(reader, img)
        if text:
            read += 1
            print(f"[{i}/{len(files)}] {base} -> {text}  ({conf:.2f})")
        rows.append([base, text, raw, round(conf, 3)])

    out_csv = os.path.join(folder, "lpr_results.csv")
    with open(out_csv, "w", newline="") as f:
        w_ = csv.writer(f)
        w_.writerow(["file", "plate", "raw_ocr", "conf"])
        w_.writerows(rows)
    print(f"\n== {len(files)} event times, {got} frames OCR'd ==")
    print(f"  KOREAN PLATE read: {read} ({100 * read // max(got, 1)}% of OCR'd)")
    print(f"  -> {out_csv}   crops: {cropdir}/  (eyeball: real plates, read correctly?)")


if __name__ == "__main__":
    main()
