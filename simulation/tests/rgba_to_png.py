#!/usr/bin/env python3
"""Lossless PNG export of a native WASM model preview; no third-party packages."""
import binascii
import pathlib
import struct
import zlib

build = pathlib.Path(__file__).resolve().parents[1] / "build"
width, height = 760, 520
rgba = (build / "preview.rgba").read_bytes()
assert len(rgba) == width * height * 4


def chunk(kind, payload):
    return (struct.pack(">I", len(payload)) + kind + payload +
            struct.pack(">I", binascii.crc32(kind + payload) & 0xFFFFFFFF))


rows = b"".join(b"\0" + rgba[row * width * 4:(row + 1) * width * 4]
                for row in range(height))
png = b"\x89PNG\r\n\x1a\n"
png += chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
png += chunk(b"IDAT", zlib.compress(rows, 9))
png += chunk(b"IEND", b"")
(build / "preview.png").write_bytes(png)
(build / "preview.rgba").unlink()
print(f"WASM model preview: {build / 'preview.png'} (not an ESP32/Wokwi runtime screenshot)")
