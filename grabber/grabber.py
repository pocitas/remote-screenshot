import asyncio
import json
import os
from typing import Optional

import cv2
import numpy as np
from skimage.metrics import structural_similarity
import websockets

SERVER_WS_URL = os.getenv("SERVER_WS_URL", "ws://localhost:8080/ws/grabber")
GRABBER_PSK = os.getenv("GRABBER_PSK", "change-me")
RECONNECT_DELAY_SECONDS = int(os.getenv("RECONNECT_DELAY_SECONDS", "5"))
SIMILARITY_THRESHOLD = float(os.getenv("SIMILARITY_THRESHOLD", "0.80"))
REFERENCE_IMAGE_PATHS = (
    os.getenv("REFERENCE_IMAGE_1", "references/ref1.jpg"),
    os.getenv("REFERENCE_IMAGE_2", "references/ref2.jpg"),
    os.getenv("REFERENCE_IMAGE_3", "references/ref3.jpg"),
)
PLACEHOLDER_WIDTH = int(os.getenv("PLACEHOLDER_WIDTH", "1280"))
PLACEHOLDER_HEIGHT = int(os.getenv("PLACEHOLDER_HEIGHT", "720"))

REFERENCE_IMAGES: list[np.ndarray] = []


def load_reference_images() -> None:
    loaded_images: list[np.ndarray] = []
    for path in REFERENCE_IMAGE_PATHS:
        image = cv2.imread(path, cv2.IMREAD_GRAYSCALE)
        if image is None:
            print(f"failed to load reference image: {path}")
            continue
        loaded_images.append(image)
    REFERENCE_IMAGES.clear()
    REFERENCE_IMAGES.extend(loaded_images)
    print(f"loaded {len(REFERENCE_IMAGES)} reference image(s)")


def placeholder_jpeg_bytes() -> Optional[bytes]:
    placeholder = np.zeros((PLACEHOLDER_HEIGHT, PLACEHOLDER_WIDTH, 3), dtype=np.uint8)
    encoded_ok, buffer = cv2.imencode(".jpg", placeholder, [int(cv2.IMWRITE_JPEG_QUALITY), 85])
    if not encoded_ok:
        print("placeholder jpeg encoding failed")
        return None
    return buffer.tobytes()


def verify_frame_similarity(frame: np.ndarray) -> bool:
    if frame is None or frame.size == 0:
        return False
    if not REFERENCE_IMAGES:
        print("no reference images loaded")
        return False

    gray_frame = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
    highest_score = -1.0
    for reference_image in REFERENCE_IMAGES:
        frame_resized = cv2.resize(gray_frame, (reference_image.shape[1], reference_image.shape[0]))
        score = structural_similarity(reference_image, frame_resized)
        if score > highest_score:
            highest_score = score

    print(f"highest SSIM score={highest_score:.4f} threshold={SIMILARITY_THRESHOLD:.4f}")
    return highest_score >= SIMILARITY_THRESHOLD


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

    if not verify_frame_similarity(frame):
        print("frame did not pass SSIM validation, sending placeholder")
        return placeholder_jpeg_bytes()

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
    load_reference_images()
    asyncio.run(run_forever())
