"""Samantha's isolated adapter for the official qwen-tts package."""

from __future__ import annotations

import argparse
from contextlib import redirect_stdout
import json
import os
import sys


VOICES = [
    "Vivian",
    "Serena",
    "Uncle_Fu",
    "Dylan",
    "Eric",
    "Ryan",
    "Aiden",
    "Ono_Anna",
    "Sohee",
]
LANGUAGES = [
    "Auto",
    "Chinese",
    "English",
    "Japanese",
    "Korean",
    "German",
    "French",
    "Russian",
    "Portuguese",
    "Spanish",
    "Italian",
]


def download(args: argparse.Namespace) -> None:
    from huggingface_hub import snapshot_download

    snapshot_download(
        repo_id=args.model_id,
        revision=args.revision,
        local_dir=args.model_dir,
    )


def capabilities(args: argparse.Namespace) -> None:
    require_model(args.model)
    model = load_model(args.model)
    message = ready_message(args.model, model)
    message["type"] = "capabilities"
    message["family"] = "customvoice"
    message["modes"] = [
        {
            "id": "customvoice",
            "voices": message["voices"],
            "supports_instruction": False,
        }
    ]
    print(json.dumps(message))


def load_model(path: str):
    # Third-party model imports print optional dependency and accelerator
    # notices. Keep stdout reserved for Samantha's JSONL protocol so those
    # diagnostics cannot be mistaken for a handshake or response.
    with redirect_stdout(sys.stderr):
        import torch
        from qwen_tts import Qwen3TTSModel

        if torch.cuda.is_available():
            device_map = "cuda:0"
            dtype = torch.bfloat16
        elif getattr(torch.backends, "mps", None) and torch.backends.mps.is_available():
            device_map = "mps"
            dtype = torch.float32
        else:
            device_map = "cpu"
            dtype = torch.float32
        model = Qwen3TTSModel.from_pretrained(
            path,
            device_map=device_map,
            dtype=dtype,
        )
    return model


def synthesize(args: argparse.Namespace) -> None:
    import soundfile as sf

    require_model(args.model)
    if args.speaker not in VOICES:
        raise ValueError(f"unsupported CustomVoice speaker: {args.speaker}")
    if args.language not in LANGUAGES:
        raise ValueError(f"unsupported language: {args.language}")
    with open(args.text_file, "r", encoding="utf-8") as handle:
        text = handle.read()
    if not text.strip():
        raise ValueError("text is empty")

    model = load_model(args.model)
    kwargs = {
        "text": text,
        "language": args.language,
        "speaker": args.speaker,
    }
    if args.instruction:
        kwargs["instruct"] = args.instruction
    wavs, sample_rate = model.generate_custom_voice(**kwargs)
    sf.write(args.output, wavs[0], sample_rate)


def ready_message(model_path: str, model) -> dict:
    voices = list(model.get_supported_speakers()) or VOICES
    languages = list(model.get_supported_languages()) or LANGUAGES
    return {
        "protocol": "samantha-qwen/v1",
        "type": "ready",
        "model": model_path,
        "sample_rate": 24000,
        "voices": voices,
        "languages": languages,
    }


def serve(args: argparse.Namespace) -> None:
    import soundfile as sf

    require_model(args.model)
    model = load_model(args.model)
    voices = list(model.get_supported_speakers()) or VOICES
    languages = list(model.get_supported_languages()) or LANGUAGES
    print(json.dumps(ready_message(args.model, model)), flush=True)
    for line in sys.stdin:
        request_id = ""
        try:
            request = json.loads(line)
            request_id = request.get("request_id", "")
            if request.get("protocol") != "samantha-qwen/v1":
                raise ValueError("unsupported worker protocol")
            if request.get("type") == "shutdown":
                return
            if request.get("type") != "synthesize":
                raise ValueError("unsupported worker request")
            speaker = request.get("voice") or "Vivian"
            language = request.get("language") or "Auto"
            canonical_speaker = next((v for v in voices if v.lower() == speaker.lower()), None)
            canonical_language = next((v for v in languages if v.lower() == language.lower()), None)
            if canonical_speaker is None:
                raise ValueError(f"unsupported CustomVoice speaker: {speaker}")
            if canonical_language is None:
                raise ValueError(f"unsupported language: {language}")
            kwargs = {
                "text": request.get("text", ""),
                "language": canonical_language,
                "speaker": canonical_speaker,
            }
            if request.get("instruction"):
                kwargs["instruct"] = request["instruction"]
            wavs, sample_rate = model.generate_custom_voice(**kwargs)
            sf.write(request["output_path"], wavs[0], sample_rate)
            response = {
                "protocol": "samantha-qwen/v1",
                "type": "complete",
                "request_id": request_id,
                "sample_rate": sample_rate,
            }
        except Exception as exc:
            response = {
                "protocol": "samantha-qwen/v1",
                "type": "error",
                "request_id": request_id,
                "error_kind": "worker_failure",
                "message": f"{type(exc).__name__}: {exc}",
            }
        print(json.dumps(response), flush=True)


def require_model(path: str) -> None:
    required = [
        os.path.join(path, "config.json"),
        os.path.join(path, "model.safetensors"),
        os.path.join(path, "speech_tokenizer", "model.safetensors"),
    ]
    missing = [item for item in required if not os.path.isfile(item)]
    if missing:
        raise FileNotFoundError("incomplete Qwen model: " + ", ".join(missing))


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(prog="samantha-qwen-worker")
    sub = root.add_subparsers(dest="command", required=True)

    install = sub.add_parser("download")
    install.add_argument("--model-id", required=True)
    install.add_argument("--revision", required=True)
    install.add_argument("--model-dir", required=True)
    install.set_defaults(func=download)

    caps = sub.add_parser("capabilities")
    caps.add_argument("--model", required=True)
    caps.set_defaults(func=capabilities)

    speak = sub.add_parser("synthesize")
    speak.add_argument("--model", required=True)
    speak.add_argument("--text-file", required=True)
    speak.add_argument("--output", required=True)
    speak.add_argument("--speaker", required=True)
    speak.add_argument("--language", default="Auto")
    speak.add_argument("--instruction", default="")
    speak.set_defaults(func=synthesize)

    server = sub.add_parser("serve")
    server.add_argument("--model", required=True)
    server.set_defaults(func=serve)
    return root


def main() -> int:
    args = parser().parse_args()
    try:
        args.func(args)
    except Exception as exc:  # stderr is captured and bounded by Samantha.
        print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
