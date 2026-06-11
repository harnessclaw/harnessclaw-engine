#!/usr/bin/env python3
"""Local image generation proxy + mock server for HarnessClaw manual tests.

The proxy accepts the same image generation paths used by the engine manifest:

  - /v1/images/generations  (GPT Image / OpenAI-compatible)
  - /images/generations     (Ark / Seedream-compatible)

It logs a compact official-parameter comparison, forwards to the local mock
server, and the mock returns PNG bytes as b64_json. No external dependencies.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import signal
import struct
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
import zlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


SUPPORTED_PATHS = {"/v1/images/generations", "/images/generations"}


def make_png(width: int = 384, height: int = 384) -> bytes:
    """Create a simple deterministic RGB PNG without Pillow."""

    def chunk(kind: bytes, data: bytes) -> bytes:
        return (
            struct.pack(">I", len(data))
            + kind
            + data
            + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
        )

    rows = []
    for y in range(height):
        row = bytearray([0])
        for x in range(width):
            if (x // 32 + y // 32) % 2 == 0:
                row.extend((238, 246, 255))
            else:
                row.extend((219, 234, 254))
        rows.append(bytes(row))
    raw = b"".join(rows)
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")


PNG_B64 = base64.b64encode(make_png()).decode("ascii")


def now() -> int:
    return int(time.time())


def read_json(handler: BaseHTTPRequestHandler) -> dict[str, Any]:
    length = int(handler.headers.get("Content-Length", "0") or "0")
    body = handler.rfile.read(length) if length > 0 else b"{}"
    try:
        parsed = json.loads(body.decode("utf-8"))
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON body: {exc}") from exc
    if not isinstance(parsed, dict):
        raise ValueError("JSON body must be an object")
    return parsed


def write_json(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]) -> None:
    data = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(data)))
    handler.end_headers()
    handler.wfile.write(data)


def official_diff(path: str, body: dict[str, Any]) -> dict[str, Any]:
    provider = "ark_seedream" if path == "/images/generations" else "openai_gpt_image"
    sent = sorted(body.keys())
    warnings: list[str] = []

    if not body.get("model"):
        warnings.append("missing required model")
    if not body.get("prompt"):
        warnings.append("missing required prompt")

    if provider == "openai_gpt_image":
        official = {
            "required": ["model", "prompt"],
            "common_optional": [
                "background",
                "moderation",
                "n",
                "output_compression",
                "output_format",
                "partial_images",
                "quality",
                "size",
                "stream",
                "user",
            ],
            "response_note": "GPT image models return b64_json by default; url is not supported for GPT image models.",
        }
        if "response_format" in body:
            warnings.append("response_format is legacy/OpenAI-DALL-E-style; GPT image models return b64_json by default")
        if body.get("size") == "512x512":
            warnings.append("512x512 is not a GPT image size in current official docs")
    else:
        official = {
            "required": ["model", "prompt"],
            "common_optional": [
                "image",
                "size",
                "response_format",
                "output_format",
                "watermark",
                "stream",
                "sequential_image_generation",
                "sequential_image_generation_options",
                "optimize_prompt_options",
            ],
            "response_note": "Seedream supports response_format=url or b64_json; Seedream 5.0 Lite commonly uses size 2K/3K/4K or large pixel dimensions.",
        }
        size = body.get("size")
        if isinstance(size, str) and size in {"512x512", "1024x1024", "1024x1536", "1536x1024"}:
            warnings.append("size is accepted by this mock, but may be too small for real Seedream 5.0 Lite")
        if "n" in body:
            warnings.append("Seedream group output uses sequential_image_generation rather than OpenAI-style n")

    return {"provider": provider, "sent": sent, "official": official, "warnings": warnings}


class MockHandler(BaseHTTPRequestHandler):
    server_version = "HarnessClawImageGenMock/1.0"

    def do_GET(self) -> None:
        if self.path == "/health":
            write_json(self, 200, {"ok": True, "role": "mock"})
            return
        if self.path.startswith("/mock-images/"):
            data = base64.b64decode(PNG_B64)
            self.send_response(200)
            self.send_header("Content-Type", "image/png")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        write_json(self, 404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path not in SUPPORTED_PATHS:
            write_json(self, 404, {"error": "unsupported path", "path": self.path})
            return
        try:
            body = read_json(self)
        except ValueError as exc:
            write_json(self, 400, {"error": str(exc)})
            return

        prompt = str(body.get("prompt") or "").strip()
        model = str(body.get("model") or "").strip()
        if not prompt or not model:
            write_json(self, 400, {"error": "model and prompt are required"})
            return

        requested_n = body.get("n", 1)
        try:
            count = int(requested_n)
        except (TypeError, ValueError):
            count = 1
        count = max(1, min(count, 4))

        response_format = body.get("response_format", "b64_json")
        image_id = uuid.uuid4().hex[:10]
        data = []
        for idx in range(count):
            item: dict[str, Any] = {
                "revised_prompt": f"[mock] {prompt}",
                "size": body.get("size", "1024x1024"),
            }
            if response_format == "url":
                item["url"] = f"http://127.0.0.1:{self.server.server_port}/mock-images/{image_id}-{idx}.png"
            else:
                item["b64_json"] = PNG_B64
            data.append(item)

        write_json(
            self,
            200,
            {
                "created": now(),
                "model": model,
                "data": data,
                "size": body.get("size", "1024x1024"),
                "quality": body.get("quality"),
                "output_format": body.get("output_format", "png"),
                "usage": {
                    "input_tokens": 8,
                    "output_tokens": 16,
                    "total_tokens": 24,
                },
            },
        )

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"[mock] {self.address_string()} - {fmt % args}", flush=True)


class ProxyHandler(BaseHTTPRequestHandler):
    server_version = "HarnessClawImageGenProxy/1.0"
    mock_base = ""

    def do_GET(self) -> None:
        if self.path == "/health":
            write_json(self, 200, {"ok": True, "role": "proxy", "mock_base": self.mock_base})
            return
        write_json(self, 404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path not in SUPPORTED_PATHS:
            write_json(self, 404, {"error": "unsupported path", "path": self.path})
            return
        try:
            body = read_json(self)
        except ValueError as exc:
            write_json(self, 400, {"error": str(exc)})
            return

        diff = official_diff(self.path, body)
        print(json.dumps({"event": "imagegen.request", "path": self.path, **diff}, ensure_ascii=False), flush=True)

        payload = json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(
            self.mock_base + self.path,
            data=payload,
            method="POST",
            headers={
                "Content-Type": "application/json",
                "Authorization": self.headers.get("Authorization", ""),
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=20) as response:
                data = response.read()
                status = response.status
                content_type = response.headers.get("Content-Type", "application/json")
        except urllib.error.HTTPError as exc:
            data = exc.read()
            status = exc.code
            content_type = exc.headers.get("Content-Type", "application/json")
        except Exception as exc:
            write_json(self, 502, {"error": f"proxy forward failed: {exc}"})
            return

        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"[proxy] {self.address_string()} - {fmt % args}", flush=True)


def serve(server: ThreadingHTTPServer) -> None:
    try:
        server.serve_forever()
    finally:
        server.server_close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--proxy-port", type=int, default=int(os.getenv("IMAGEGEN_PROXY_PORT", "18181")))
    parser.add_argument("--mock-port", type=int, default=int(os.getenv("IMAGEGEN_MOCK_PORT", "18182")))
    args = parser.parse_args()

    mock = ThreadingHTTPServer((args.host, args.mock_port), MockHandler)
    ProxyHandler.mock_base = f"http://{args.host}:{args.mock_port}"
    proxy = ThreadingHTTPServer((args.host, args.proxy_port), ProxyHandler)

    stop = threading.Event()

    def shutdown(_signum: int, _frame: Any) -> None:
        stop.set()
        mock.shutdown()
        proxy.shutdown()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)

    threads = [
        threading.Thread(target=serve, args=(mock,), daemon=True),
        threading.Thread(target=serve, args=(proxy,), daemon=True),
    ]
    for thread in threads:
        thread.start()

    print(
        json.dumps(
            {
                "event": "imagegen.local.ready",
                "proxy_base_url": f"http://{args.host}:{args.proxy_port}",
                "mock_base_url": f"http://{args.host}:{args.mock_port}",
                "paths": sorted(SUPPORTED_PATHS),
            },
            ensure_ascii=False,
        ),
        flush=True,
    )

    while not stop.is_set():
        time.sleep(0.25)
    return 0


if __name__ == "__main__":
    sys.exit(main())
