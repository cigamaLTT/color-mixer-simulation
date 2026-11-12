"""MQTT 3.1.1 and WebSocket clients for integration tests."""

import base64
import hashlib
import json
import os
import socket
import struct
import time
import urllib.parse


def _exact(sock, size):
    chunks = bytearray()
    while len(chunks) < size:
        chunk = sock.recv(size - len(chunks))
        if not chunk:
            raise EOFError("peer closed the connection")
        chunks.extend(chunk)
    return bytes(chunks)


class WebSocket:
    def __init__(self, url, subprotocol=None, timeout=3):
        parsed = urllib.parse.urlparse(url)
        if parsed.scheme != "ws":
            raise ValueError("integration transport expects a local ws:// URL")
        self.sock = socket.create_connection((parsed.hostname, parsed.port or 80), timeout)
        self.sock.settimeout(timeout)
        self.buffer = bytearray()
        self.stream_buffer = bytearray()
        key = base64.b64encode(os.urandom(16)).decode()
        path = parsed.path or "/"
        if parsed.query:
            path += "?" + parsed.query
        headers = [
            f"GET {path} HTTP/1.1", f"Host: {parsed.netloc}",
            "Upgrade: websocket", "Connection: Upgrade",
            f"Sec-WebSocket-Key: {key}", "Sec-WebSocket-Version: 13",
            f"Origin: http://{parsed.netloc}",
        ]
        if subprotocol:
            headers.append(f"Sec-WebSocket-Protocol: {subprotocol}")
        self.sock.sendall(("\r\n".join(headers) + "\r\n\r\n").encode())
        response = bytearray()
        while b"\r\n\r\n" not in response:
            part = self.sock.recv(4096)
            if not part:
                raise ConnectionError("WebSocket handshake closed early")
            response.extend(part)
            if len(response) > 65536:
                raise ConnectionError("oversized WebSocket handshake")
        header, tail = bytes(response).split(b"\r\n\r\n", 1)
        self.buffer.extend(tail)
        if not header.startswith(b"HTTP/1.1 101 "):
            raise ConnectionError(header.decode(errors="replace"))
        expected = base64.b64encode(hashlib.sha1(
            (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()
        ).digest()).decode()
        fields = dict(line.decode().split(":", 1) for line in header.split(b"\r\n")[1:] if b":" in line)
        fields = {name.lower(): value.strip() for name, value in fields.items()}
        if fields.get("sec-websocket-accept") != expected:
            raise ConnectionError("invalid WebSocket accept hash")
        if subprotocol and fields.get("sec-websocket-protocol") != subprotocol:
            raise ConnectionError("MQTT WebSocket subprotocol was not negotiated")

    def settimeout(self, timeout):
        self.sock.settimeout(timeout)

    def _read(self, size):
        while len(self.buffer) < size:
            part = self.sock.recv(max(4096, size - len(self.buffer)))
            if not part:
                raise EOFError("WebSocket peer closed")
            self.buffer.extend(part)
        result = bytes(self.buffer[:size])
        del self.buffer[:size]
        return result

    def send_frame(self, payload, opcode=2):
        length = len(payload)
        header = bytearray([0x80 | opcode])
        if length < 126:
            header.append(0x80 | length)
        elif length <= 65535:
            header.append(0x80 | 126)
            header.extend(struct.pack("!H", length))
        else:
            header.append(0x80 | 127)
            header.extend(struct.pack("!Q", length))
        mask = os.urandom(4)
        header.extend(mask)
        masked = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
        self.sock.sendall(header + masked)

    def sendall(self, payload):
        self.send_frame(payload, opcode=2)

    def send_json(self, payload):
        self.send_frame(json.dumps(payload).encode(), opcode=1)

    def receive(self):
        result = bytearray()
        while True:
            first, second = self._read(2)
            final, opcode, masked = first & 0x80, first & 15, second & 0x80
            length = second & 127
            if length == 126:
                length = struct.unpack("!H", self._read(2))[0]
            elif length == 127:
                length = struct.unpack("!Q", self._read(8))[0]
            if length > 4 * 1024 * 1024:
                raise ValueError("oversized WebSocket frame")
            mask = self._read(4) if masked else None
            payload = self._read(length)
            if mask:
                payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
            if opcode == 8:
                raise EOFError("WebSocket close frame")
            if opcode == 9:
                self.send_frame(payload, opcode=10)
                continue
            if opcode == 10:
                continue
            if opcode not in (0, 1, 2):
                raise ValueError(f"unsupported WebSocket opcode {opcode}")
            result.extend(payload)
            if final:
                return bytes(result)

    def receive_json(self):
        return json.loads(self.receive())

    def recv(self, size):
        while not self.stream_buffer:
            self.stream_buffer.extend(self.receive())
        result = bytes(self.stream_buffer[:size])
        del self.stream_buffer[:size]
        return result

    def close(self):
        try:
            self.send_frame(b"", opcode=8)
        except OSError:
            pass
        self.sock.close()

    def __enter__(self):
        return self

    def __exit__(self, *unused):
        self.close()


def _string(value):
    raw = value.encode()
    return struct.pack("!H", len(raw)) + raw


def _packet(header, body):
    remaining = len(body)
    encoded = bytearray()
    while True:
        digit = remaining % 128
        remaining //= 128
        encoded.append(digit | (128 if remaining else 0))
        if not remaining:
            return bytes([header]) + encoded + body


class MQTT:
    def __init__(self, host, port, client_id, websocket=False):
        self.socket = (WebSocket(f"ws://{host}:{port}/mqtt", subprotocol="mqtt")
                       if websocket else socket.create_connection((host, port), timeout=3))
        self.socket.settimeout(3)
        self.packet_id = 0
        self.backlog = []
        body = _string("MQTT") + bytes([4, 2, 0, 30]) + _string(client_id)
        self.socket.sendall(_packet(0x10, body))
        header, payload = self._read_packet()
        if header != 0x20 or payload != b"\x00\x00":
            raise ConnectionError(f"MQTT rejected connection: {header:#x} {payload!r}")

    def _next_id(self):
        self.packet_id = self.packet_id % 65535 + 1
        return self.packet_id

    def _read_packet(self):
        header = _exact(self.socket, 1)[0]
        remaining, multiplier = 0, 1
        for unused in range(4):
            digit = _exact(self.socket, 1)[0]
            remaining += (digit & 127) * multiplier
            if not digit & 128:
                return header, _exact(self.socket, remaining)
            multiplier *= 128
        raise ValueError("malformed MQTT remaining length")

    def subscribe(self, topic):
        packet_id = self._next_id()
        self.socket.sendall(_packet(0x82, struct.pack("!H", packet_id) + _string(topic) + b"\x01"))
        while True:
            header, body = self._read_packet()
            if header == 0x90 and body[:2] == struct.pack("!H", packet_id):
                if body[2:] not in (b"\x00", b"\x01"):
                    raise ConnectionError(f"MQTT subscription rejected: {body!r}")
                return
            self.backlog.append((header, body))

    def publish(self, topic, payload, retain=False):
        if not isinstance(payload, bytes):
            payload = json.dumps(payload, separators=(",", ":")).encode()
        self.socket.sendall(_packet(0x31 if retain else 0x30, _string(topic) + payload))

    def receive(self, timeout=3):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.socket.settimeout(max(0.01, end - time.monotonic()))
            header, body = self.backlog.pop(0) if self.backlog else self._read_packet()
            if header >> 4 != 3:
                continue
            length = struct.unpack("!H", body[:2])[0]
            topic = body[2:2 + length].decode()
            offset = 2 + length
            qos = (header >> 1) & 3
            if qos:
                packet_id = body[offset:offset + 2]
                offset += 2
                if qos != 1:
                    raise ValueError("test MQTT client only accepts QoS0/1")
                self.socket.sendall(_packet(0x40, packet_id))
            return {"topic": topic, "payload": body[offset:], "retain": bool(header & 1), "qos": qos}
        raise TimeoutError("MQTT receive deadline exceeded")

    def close(self):
        try:
            self.socket.sendall(b"\xe0\x00")
        except OSError:
            pass
        self.socket.close()

    def __enter__(self):
        return self

    def __exit__(self, *unused):
        self.close()


def until_json(connection, predicate, timeout=5):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        connection.settimeout(max(0.01, end - time.monotonic()))
        message = connection.receive_json()
        if predicate(message):
            return message
    raise TimeoutError("expected application WebSocket message did not arrive")
