# LPR eval — Korean plate recognition on real DVR frames (Unraid + RTX 4080)

A throwaway evaluator: run [EasyKoreanLpDetector](https://github.com/gyupro/EasyKoreanLpDetector)
(YOLOv5 plate detect + EasyOCR Korean recogniser) over a folder of JPEGs and get a
CSV, to answer **"does a Korean LPR model read our cameras' plates well enough?"**
*before* any relay integration. License-of-the-upstream-repo: caller's call.

## Where the frames are

The relay already stores per-channel **event snapshots** (motion-edge JPEGs) on disk:

```
<RELAY_REC_HOST share>/<agent>/dvr<N>_ch<M>/.evthumbs/<unixsec>.jpg
```

So on Unraid you can point the evaluator straight at that folder — no download. (Or
pull them to a PC via `GET /dashboard/api/lpr-frames?stream=<agent>/dvr<N>_ch<M>&n=300`,
which zips the newest N.)

## Run (on the Unraid box — needs the **Nvidia-Driver** plugin for `--gpus`)

```sh
cd /mnt/user/stack/OpsView/tools/lpr-eval
docker build -t lpr-eval .

# ramp camera (the hard case — night bloom/glare):
docker run --rm --gpus all \
  -v /mnt/user/<recshare>/<agent>/dvr0_ch1/.evthumbs:/frames \
  lpr-eval

# indoor camera (the readable one):
docker run --rm --gpus all \
  -v /mnt/user/<recshare>/<agent>/dvr0_ch4/.evthumbs:/frames \
  lpr-eval
```

Results land at `/frames/lpr_results.csv` (= inside that `.evthumbs` dir):
`file,plate,conf`. **Eyeball it**: how many plates are actually *correct* — compare
ch4 (indoor, frontal) vs ch1 (ramp, night). That ratio is the go/no-go.

## Reading the result

- **ch4 reads well** → capture is fine for those; integrate as a Python sidecar the
  relay calls (`runLPR` → HTTP). Use = tag events with plate / search recordings by plate.
- **ch1 night fails** → capture (exposure/IR/angle) is the bottleneck, not the model;
  fix that first, or accept indoor-only.
- **Both weak** → consider fine-tuning the detector (YOLOv5) + recogniser on labelled
  crops from these same frames (the 4080 can train).

Automation of the 기계식 주차장 (입출고) is **out** — the controller can't take external
plate input — so the only value here is plate tagging / search.
