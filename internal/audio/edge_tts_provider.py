"""Stream Edge TTS audio and its original WordBoundary metadata to files."""

import argparse
import asyncio
import json
import sys

import edge_tts


async def synthesize(args: argparse.Namespace) -> None:
    text = sys.stdin.read()
    if not text.strip():
        raise ValueError("text must not be empty")

    boundaries = []
    # edge-tts 7.2 defaults to SentenceBoundary. Request words explicitly so
    # an upstream default change cannot silently degrade subtitle alignment.
    communicate = edge_tts.Communicate(text, args.voice, boundary="WordBoundary")
    with open(args.media, "wb") as media:
        async for chunk in communicate.stream():
            if chunk["type"] == "audio":
                media.write(chunk["data"])
            elif chunk["type"] == "WordBoundary":
                boundaries.append(
                    {
                        "text": chunk["text"],
                        # Edge reports offsets and durations in 100 ns ticks.
                        "start_ms": round(chunk["offset"] / 10_000),
                        "end_ms": round(
                            (chunk["offset"] + chunk["duration"]) / 10_000
                        ),
                    }
                )

    if not boundaries:
        raise RuntimeError("edge-tts returned no WordBoundary metadata")
    with open(args.timings, "w", encoding="utf-8") as timings:
        json.dump(boundaries, timings, ensure_ascii=False)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--voice", required=True)
    parser.add_argument("--media", required=True)
    parser.add_argument("--timings", required=True)
    asyncio.run(synthesize(parser.parse_args()))


if __name__ == "__main__":
    main()
