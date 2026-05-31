import asyncio
import json
import os
from typing import Optional

import cv2
import numpy as np
import websockets

SERVER_WS_URL = os.getenv("SERVER_WS_URL", "ws://localhost:8080/ws/grabber")
GRABBER_PSK = os.getenv("GRABBER_PSK", "change-me")
RECONNECT_DELAY_SECONDS = int(os.getenv("RECONNECT_DELAY_SECONDS", "5"))


def verify_frame_looks_like_gantt(frame: np.ndarray) -> bool:
    """Basic placeholder verification for a Gantt/grid-like image."""
    if frame is None or frame.size == 0:
        return False

    height, width = frame.shape[:2]
    if width < 640 or height < 360:
        return False

    gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
    edges = cv2.Canny(gray, 50, 150)
    edge_density = float(np.count_nonzero(edges)) / float(edges.size)

    # Placeholder heuristic: typical UIs have moderate edge density.
    # Replace with template matching against known reference UI regions.
    return 0.01 < edge_density < 0.35


def capture_jpeg_bytes() -> Optional[bytes]:
    cap = cv2.VideoCapture(0)
    if not cap.isOpened():
        print("video capture device not available")
        return None

    ok, frame = cap.read()
    cap.release()
    if not ok or frame is None:
        print("failed to read frame")
        return None

    if not verify_frame_looks_like_gantt(frame):
        print("frame did not pass gantt/grid verification")
        return None

    encoded_ok, buffer = cv2.imencode(".jpg", frame, [int(cv2.IMWRITE_JPEG_QUALITY), 85])
    if not encoded_ok:
        print("jpeg encoding failed")
        return None

    return buffer.tobytes()


async def handle_messages(ws: websockets.WebSocketClientProtocol) -> None:
    async for message in ws:
        if isinstance(message, bytes):
            # Server should only send JSON text commands.
            continue

        try:
            payload = json.loads(message)
        except json.JSONDecodeError:
            print("invalid JSON command")
            continue

        if payload.get("cmd") != "capture":
            continue

        image_bytes = capture_jpeg_bytes()
        if image_bytes is None:
            continue

        await ws.send(image_bytes)
        print(f"sent screenshot ({len(image_bytes)} bytes)")


async def run_forever() -> None:
    headers = {"X-Grabber-PSK": GRABBER_PSK}
    while True:
        try:
            print(f"connecting to {SERVER_WS_URL}")
            async with websockets.connect(SERVER_WS_URL, additional_headers=headers) as ws:
                print("connected to server")
                await handle_messages(ws)
        except Exception as exc:  # noqa: BLE001
            print(f"connection error: {exc}")
            await asyncio.sleep(RECONNECT_DELAY_SECONDS)


if __name__ == "__main__":
    asyncio.run(run_forever())
