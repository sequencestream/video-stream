"""faster-whisper recognition helper, executed by vs.

vs writes this file to a temporary path, runs it with the configured Python,
sends a JSON request on stdin and reads a JSON response from stdout. Progress
goes to stderr so it never contaminates the result.

Keeping it out-of-process is what lets vs stay a single Go binary while still
using the Python speech stack: no service to start, no port to configure, and
a missing dependency is reported as a sentence rather than a stack trace.
"""

from __future__ import annotations

import json
import sys


def fail(message: str, hint: str = "") -> int:
    json.dump({"error": message, "hint": hint}, sys.stdout, ensure_ascii=False)
    sys.stdout.flush()
    return 2


def main() -> int:
    try:
        request = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        return fail(f"could not parse the request: {exc}")

    try:
        from faster_whisper import WhisperModel
    except ImportError:
        return fail(
            "the faster-whisper package is not installed for this interpreter",
            f"{sys.executable} -m pip install faster-whisper",
        )

    kwargs = {"device": request.get("device") or "auto"}
    if request.get("compute_type"):
        kwargs["compute_type"] = request["compute_type"]
    if request.get("model_dir"):
        kwargs["download_root"] = request["model_dir"]
    if request.get("threads"):
        kwargs["cpu_threads"] = int(request["threads"])

    try:
        model = WhisperModel(request["model"], **kwargs)
    except Exception as exc:  # noqa: BLE001 - surface whatever went wrong verbatim
        return fail(
            f"could not load model {request.get('model')!r}: {exc}",
            "the first run downloads the model, which needs network access",
        )

    transcribe_kwargs = {
        "word_timestamps": True,
        "vad_filter": bool(request.get("vad", True)),
        "beam_size": int(request.get("beam_size") or 5),
    }
    if request.get("language"):
        transcribe_kwargs["language"] = request["language"]
    if request.get("prompt"):
        transcribe_kwargs["initial_prompt"] = request["prompt"]

    try:
        segments, info = model.transcribe(request["audio"], **transcribe_kwargs)
    except Exception as exc:  # noqa: BLE001
        return fail(f"transcription failed: {exc}")

    total = float(getattr(info, "duration", 0.0) or 0.0)
    show_progress = bool(request.get("progress"))
    progress_width = 0
    cues = []

    # segments is a generator: recognition happens as it is consumed, which is
    # what makes reporting progress possible at all.
    for segment in segments:
        words = []
        for word in segment.words or []:
            text = (word.word or "").strip()
            if not text:
                continue
            words.append(
                {
                    "text": text,
                    "start_ms": int(word.start * 1000 + 0.5),
                    "end_ms": int(word.end * 1000 + 0.5),
                    "score": round(float(word.probability or 0.0), 4),
                }
            )
        text = (segment.text or "").strip()
        if not text and not words:
            continue
        cues.append(
            {
                "start_ms": int(segment.start * 1000 + 0.5),
                "end_ms": int(segment.end * 1000 + 0.5),
                "text": text,
                "words": words,
            }
        )
        if show_progress and total > 0:
            done = min(segment.end, total)
            line = f"transcribing {done:6.1f}s / {total:.1f}s ({done / total * 100:5.1f}%)"
            sys.stderr.write("\r" + line)
            sys.stderr.flush()
            progress_width = len(line)

    # Overwrite the progress line with spaces rather than an erase escape: the
    # escape prints as literal "[K" whenever stderr is not a terminal, which is
    # exactly where the output is being read rather than watched.
    if show_progress and progress_width:
        sys.stderr.write("\r" + " " * progress_width + "\r")
        sys.stderr.flush()

    json.dump(
        {
            "language": getattr(info, "language", "") or "",
            "language_probability": round(float(getattr(info, "language_probability", 0.0) or 0.0), 4),
            "duration_ms": int(total * 1000 + 0.5),
            "cues": cues,
        },
        sys.stdout,
        ensure_ascii=False,
    )
    sys.stdout.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
