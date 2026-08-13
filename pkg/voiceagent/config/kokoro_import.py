#!/usr/bin/env python3
"""Convert thewh1teagle Kokoro v1.0 assets into a sherpa-onnx OfflineTts layout.

Python samantha-cli uses:
  - kokoro-v1.0.onnx + voices-v1.0.bin (NPZ) from thewh1teagle/kokoro-onnx

Go samantha/sherpa expects:
  - model.onnx with sample_rate / n_speakers metadata
  - voices.bin as raw float32 [n_speakers, 510, 1, 256]
  - tokens.txt, espeak-ng-data, lexicon (copied from the multi-lang pack)

This script builds models_dir/kokoro-v1.0-en/ from those pieces.
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

SPEAKERS = [
    "af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica",
    "af_kore", "af_nicole", "af_nova", "af_river", "af_sarah", "af_sky",
    "am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
    "am_michael", "am_onyx", "am_puck", "am_santa",
    "bf_alice", "bf_emma", "bf_isabella", "bf_lily",
    "bm_daniel", "bm_fable", "bm_george", "bm_lewis",
]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--onnx", type=Path, required=True, help="kokoro-v1.0.onnx")
    ap.add_argument("--voices-npz", type=Path, required=True, help="voices-v1.0.bin (NPZ)")
    ap.add_argument("--frontend-dir", type=Path, required=True, help="dir with tokens.txt + espeak-ng-data")
    ap.add_argument("--out-dir", type=Path, required=True)
    args = ap.parse_args()

    try:
        import numpy as np
        import onnx
    except ImportError as e:
        print(f"missing python deps (need numpy + onnx): {e}", file=sys.stderr)
        return 2

    out: Path = args.out_dir
    out.mkdir(parents=True, exist_ok=True)

    npz = np.load(args.voices_npz)
    missing = [s for s in SPEAKERS if s not in npz.files]
    if missing:
        print(f"voices NPZ missing speakers: {missing}", file=sys.stderr)
        return 1

    voices_path = out / "voices.bin"
    with voices_path.open("wb") as f:
        for s in SPEAKERS:
            arr = np.asarray(npz[s], dtype=np.float32)
            if arr.ndim == 2:
                arr = arr[:, None, :]
            if arr.shape != (510, 1, 256):
                print(f"{s}: unexpected shape {arr.shape}, want (510,1,256)", file=sys.stderr)
                return 1
            f.write(np.ascontiguousarray(arr).tobytes())

    model = onnx.load(str(args.onnx))
    del model.metadata_props[:]
    id2speaker = ",".join(f"{i}->{s}" for i, s in enumerate(SPEAKERS))
    speaker2id = ",".join(f"{s}->{i}" for i, s in enumerate(SPEAKERS))
    meta = {
        "model_type": "kokoro",
        "language": "English",
        "voice": "en-us",
        "has_espeak": "1",
        "maintainer": "samantha-kokoro-import",
        "version": "2",
        "n_speakers": str(len(SPEAKERS)),
        "style_dim": "510,1,256",
        "sample_rate": "24000",
        "id2speaker": id2speaker,
        "speaker2id": speaker2id,
        "speaker_names": ",".join(SPEAKERS),
        "comment": "Kokoro v1.0 weights from thewh1teagle/kokoro-onnx with sherpa-compatible metadata",
        "model_url": "https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0/kokoro-v1.0.onnx",
        "see_also": "https://github.com/thewh1teagle/kokoro-onnx",
    }
    for k, v in meta.items():
        p = model.metadata_props.add()
        p.key = k
        p.value = v
    onnx.save(model, str(out / "model.onnx"))

    for name in ("tokens.txt", "espeak-ng-data", "dict", "lexicon-us-en.txt", "lexicon-gb-en.txt"):
        src = args.frontend_dir / name
        dst = out / name
        if not src.exists():
            if name.startswith("lexicon-gb"):
                continue
            print(f"missing frontend file {src}", file=sys.stderr)
            return 1
        if src.is_dir():
            if dst.exists():
                shutil.rmtree(dst)
            shutil.copytree(src, dst)
        else:
            shutil.copy2(src, dst)

    (out / ".kokoro-v1-en-source").write_text(
        json.dumps(
            {
                "source": "thewh1teagle/kokoro-onnx",
                "onnx": str(args.onnx),
                "voices_npz": str(args.voices_npz),
                "speakers": SPEAKERS,
            },
            indent=2,
        )
        + "\n"
    )
    print(f"wrote sherpa-compatible pack to {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
