import asyncio
import base64
import json
import os
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

import cv2
import numpy as np
from skimage.metrics import structural_similarity
import websockets

SERVER_WS_URL = os.getenv("SERVER_WS_URL", "ws://localhost:8080/ws/grabber")
GRABBER_PSK = os.getenv("GRABBER_PSK", "change-me")
RECONNECT_DELAY_SECONDS = int(os.getenv("RECONNECT_DELAY_SECONDS", "5"))
SIMILARITY_THRESHOLD = float(os.getenv("SIMILARITY_THRESHOLD", "0.80"))
REFERENCE_IMAGES_DIR = os.getenv("REFERENCE_IMAGES_DIR", "references")
FAILED_IMAGES_DIR = os.getenv("FAILED_IMAGES_DIR", "failed_captures")
GRABBER_ID = os.getenv("GRABBER_ID", "")

REFERENCE_IMAGES: list[np.ndarray] = []
REFERENCE_EXTENSIONS = {".jpg", ".jpeg", ".png", ".bmp", ".webp"}


def ensure_reference_images_dir() -> Path:
    directory = Path(REFERENCE_IMAGES_DIR)
    directory.mkdir(parents=True, exist_ok=True)
    return directory


def list_reference_image_paths() -> list[Path]:
    directory = ensure_reference_images_dir()
    return sorted(
        [path for path in directory.iterdir() if path.is_file() and path.suffix.lower() in REFERENCE_EXTENSIONS],
        key=lambda p: p.name,
    )


def load_reference_images() -> None:
    loaded_images: list[np.ndarray] = []
    for path in list_reference_image_paths():
        image = cv2.imread(str(path), cv2.IMREAD_GRAYSCALE)
        if image is None:
            print(f"failed to load reference image: {path}")
            continue
        loaded_images.append(image)
    REFERENCE_IMAGES.clear()
    REFERENCE_IMAGES.extend(loaded_images)
    print(f"loaded {len(REFERENCE_IMAGES)} reference image(s)")


def verify_frame_similarity(frame: np.ndarray) -> dict:
    telemetry = {
        "scores": [],
        "best_score": -1.0,
        "threshold": SIMILARITY_THRESHOLD,
        "decision": "fail",
    }
    if frame is None or frame.size == 0:
        return telemetry
    if not REFERENCE_IMAGES:
        print("no reference images loaded")
        return telemetry

    gray_frame = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
    highest_score = -1.0
    scores: list[float] = []
    for reference_image in REFERENCE_IMAGES:
        frame_resized = cv2.resize(gray_frame, (reference_image.shape[1], reference_image.shape[0]))
        score = float(structural_similarity(reference_image, frame_resized))
        scores.append(score)
        if score > highest_score:
            highest_score = score

    decision = "pass" if highest_score >= SIMILARITY_THRESHOLD else "fail"
    telemetry.update(
        {
            "scores": scores,
            "best_score": highest_score,
            "threshold": SIMILARITY_THRESHOLD,
            "decision": decision,
        }
    )
    print(f"highest SSIM score={highest_score:.4f} threshold={SIMILARITY_THRESHOLD:.4f}")
    return telemetry


def save_failed_frame(frame: np.ndarray, request_id: str) -> tuple[Optional[str], Optional[bytes]]:
    now = datetime.now(timezone.utc)
    day_dir = Path(FAILED_IMAGES_DIR) / now.strftime("%Y-%m-%d")
    short_request_id = (request_id or "no-request")[:8]
    filename = f"{now.strftime('%Y%m%d_%H%M%S')}_{short_request_id}.jpg"
    relative_path = str(Path(now.strftime("%Y-%m-%d")) / filename)

    if not day_dir.exists():
        day_dir.mkdir(parents=True, exist_ok=True)

    encoded_ok, buffer = cv2.imencode(".jpg", frame, [int(cv2.IMWRITE_JPEG_QUALITY), 85])
    if not encoded_ok:
        print("jpeg encoding failed")
        return None, None

    image_bytes = buffer.tobytes()
    try:
        (day_dir / filename).write_bytes(image_bytes)
    except OSError as exc:
        print(f"failed to save failed frame: {exc}")
        return None, None

    return relative_path, image_bytes


def capture_frame() -> Optional[np.ndarray]:
    cap = cv2.VideoCapture(0)
    if not cap.isOpened():
        print("video capture device not available")
        return None

    ok, frame = cap.read()
    cap.release()
    if not ok or frame is None:
        print("failed to read frame")
        return None
    return frame


def next_reference_filename(prefix: str) -> str:
    now = datetime.now(timezone.utc)
    suffix = os.urandom(4).hex()
    return f"{prefix}_{now.strftime('%Y%m%d_%H%M%S')}_{suffix}.jpg"


def save_reference_image(frame: np.ndarray, prefix: str) -> tuple[bool, str]:
    if frame is None or frame.size == 0:
        return False, "empty frame"
    encoded_ok, buffer = cv2.imencode(".jpg", frame, [int(cv2.IMWRITE_JPEG_QUALITY), 90])
    if not encoded_ok:
        return False, "jpeg encoding failed"
    filename = next_reference_filename(prefix)
    path = ensure_reference_images_dir() / filename
    try:
        path.write_bytes(buffer.tobytes())
    except OSError as exc:
        return False, f"failed to save reference image: {exc}"
    load_reference_images()
    return True, filename


def resolve_reference_path(name: str) -> Optional[Path]:
    if not name or Path(name).name != name:
        return None
    base_dir = ensure_reference_images_dir().resolve()
    target = (base_dir / name).resolve()
    if target.parent != base_dir:
        return None
    return target


def add_reference_from_bytes(image_bytes: bytes, prefix: str) -> tuple[bool, str]:
    if not image_bytes:
        return False, "empty image payload"
    image_array = np.frombuffer(image_bytes, dtype=np.uint8)
    frame = cv2.imdecode(image_array, cv2.IMREAD_COLOR)
    if frame is None:
        return False, "invalid image payload"
    return save_reference_image(frame, prefix)


def capture_jpeg_bytes(request_id: str) -> tuple[Optional[bytes], Optional[dict], Optional[str]]:
    frame = capture_frame()
    if frame is None:
        return None, None, None

    similarity = verify_frame_similarity(frame)
    telemetry = {
        "type": "telemetry",
        "request_id": request_id,
        "timestamp": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
        "grabber_id": GRABBER_ID,
        "best_score": similarity["best_score"],
        "scores": similarity["scores"],
        "threshold": similarity["threshold"],
        "decision": similarity["decision"],
        "failed_image_filename": None,
        "failed_image_data": None,
    }

    if similarity["decision"] == "fail":
        print("frame did not pass SSIM validation")
        failed_image_filename, failed_image_bytes = save_failed_frame(frame, request_id)
        if failed_image_filename is not None and failed_image_bytes is not None:
            telemetry["failed_image_filename"] = failed_image_filename
            telemetry["failed_image_data"] = base64.b64encode(failed_image_bytes).decode("ascii")
        return None, telemetry, "Screenshot rejected by validator. A new capture will be requested automatically."

    encoded_ok, buffer = cv2.imencode(".jpg", frame, [int(cv2.IMWRITE_JPEG_QUALITY), 85])
    if not encoded_ok:
        print("jpeg encoding failed")
        return None, telemetry, None

    return buffer.tobytes(), telemetry, None


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

        cmd = str(payload.get("cmd", ""))
        request_id = str(payload.get("request_id", ""))

        if cmd == "capture":
            image_bytes, telemetry, validation_failure_message = capture_jpeg_bytes(request_id)
            if image_bytes is not None:
                await ws.send(image_bytes)
                print(f"sent screenshot ({len(image_bytes)} bytes)")
            elif validation_failure_message:
                await ws.send(
                    json.dumps(
                        {
                            "type": "capture_result",
                            "request_id": request_id,
                            "status": "validation_failed",
                            "message": validation_failure_message,
                        }
                    )
                )
                print(f"sent validation failure request_id={request_id}")
            else:
                continue

            if telemetry is not None:
                await ws.send(json.dumps(telemetry))
                print(f"sent telemetry request_id={request_id} decision={telemetry['decision']}")
            continue

        if cmd == "list_references":
            names = [path.name for path in list_reference_image_paths()]
            await ws.send(
                json.dumps(
                    {
                        "type": "reference_result",
                        "request_id": request_id,
                        "status": "ok",
                        "action": "list_references",
                        "references": names,
                    }
                )
            )
            continue

        if cmd == "get_reference":
            reference_name = str(payload.get("name", ""))
            ref_path = resolve_reference_path(reference_name)
            if ref_path is None or not ref_path.exists():
                await ws.send(
                    json.dumps(
                        {
                            "type": "reference_result",
                            "request_id": request_id,
                            "status": "error",
                            "action": "get_reference",
                            "error": "reference image not found",
                        }
                    )
                )
                continue
            try:
                data = ref_path.read_bytes()
            except OSError as exc:
                await ws.send(
                    json.dumps(
                        {
                            "type": "reference_result",
                            "request_id": request_id,
                            "status": "error",
                            "action": "get_reference",
                            "error": f"failed to read reference image: {exc}",
                        }
                    )
                )
                continue
            await ws.send(
                json.dumps(
                    {
                        "type": "reference_result",
                        "request_id": request_id,
                        "status": "ok",
                        "action": "get_reference",
                        "name": reference_name,
                        "image_base64": base64.b64encode(data).decode("ascii"),
                    }
                )
            )
            continue

        if cmd == "capture_reference":
            frame = capture_frame()
            if frame is None:
                await ws.send(
                    json.dumps(
                        {
                            "type": "reference_result",
                            "request_id": request_id,
                            "status": "error",
                            "action": "capture_reference",
                            "error": "failed to capture frame",
                        }
                    )
                )
                continue
            ok, result = save_reference_image(frame, "capture")
            status = "ok" if ok else "error"
            response: dict[str, str] = {
                "type": "reference_result",
                "request_id": request_id,
                "status": status,
                "action": "capture_reference",
            }
            if ok:
                response["name"] = result
            else:
                response["error"] = result
            await ws.send(json.dumps(response))
            continue

        if cmd == "add_reference_image":
            data_base64 = str(payload.get("image_base64", ""))
            try:
                image_bytes = base64.b64decode(data_base64, validate=True)
            except Exception:  # noqa: BLE001
                image_bytes = b""
            ok, result = add_reference_from_bytes(image_bytes, "promoted")
            status = "ok" if ok else "error"
            response = {
                "type": "reference_result",
                "request_id": request_id,
                "status": status,
                "action": "add_reference_image",
            }
            if ok:
                response["name"] = result
            else:
                response["error"] = result
            await ws.send(json.dumps(response))
            continue

        if cmd == "delete_reference":
            reference_name = str(payload.get("name", ""))
            ref_path = resolve_reference_path(reference_name)
            if ref_path is None or not ref_path.exists():
                await ws.send(
                    json.dumps(
                        {
                            "type": "reference_result",
                            "request_id": request_id,
                            "status": "error",
                            "action": "delete_reference",
                            "error": "reference image not found",
                        }
                    )
                )
                continue
            try:
                ref_path.unlink()
            except OSError as exc:
                await ws.send(
                    json.dumps(
                        {
                            "type": "reference_result",
                            "request_id": request_id,
                            "status": "error",
                            "action": "delete_reference",
                            "error": f"failed to delete reference image: {exc}",
                        }
                    )
                )
                continue
            load_reference_images()
            await ws.send(
                json.dumps(
                    {
                        "type": "reference_result",
                        "request_id": request_id,
                        "status": "ok",
                        "action": "delete_reference",
                        "name": reference_name,
                    }
                )
            )


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
