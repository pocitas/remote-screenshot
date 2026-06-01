# remote-screenshot

A small remote screenshot system with three core pieces:

- **Grabber** (`grabber/grabber.py`) connects to the server over WebSocket, captures a frame, compares it to reference images with SSIM, and sends the screenshot plus validation telemetry.
- **Server** (`server/`) exposes the screenshot API, accepts grabber connections, stores validation logs in SQLite, retains failed captures for 7 days, and serves the admin UI.
- **Admin UI** (`/admin/*`) lets operators review validation history, filter results, inspect pass/fail rates, and open failed images.

## System overview

1. A client first requests a JWT from `POST /api/gate/token` using `X-Gate-Secret`.
2. The client calls `GET /api/screenshot` with `Authorization: ******
3. The server sends a WebSocket capture command to the connected grabber and includes a generated `request_id`.
4. The grabber captures a frame, computes SSIM scores against each configured reference image, and decides `pass` or `fail`.
5. The grabber sends result messages back on the existing connection:
   - on validation pass: the JPEG image as a binary message
   - on validation fail: a JSON message with `type=capture_result`, `status=validation_failed`, and a user-safe message
   - telemetry JSON containing scores, threshold, decision, request ID, grabber ID, and failed image data if validation failed
6. The server returns either:
   - `image/jpeg` bytes when validation passes
   - JSON like `{"status":"validation_failed","message":"..."}` when validation fails
7. The server saves telemetry into SQLite, stores failed images on disk, and exposes the data in the admin UI.

## Environment variables

### Grabber

- `SERVER_WS_URL` - WebSocket endpoint for the server grabber connection. Default: `ws://localhost:8080/ws/grabber`
- `GRABBER_PSK` - shared secret for authenticating the grabber; **must exactly match server `GRABBER_PSK`**. Recommended minimum: 32 random bytes (64 preferred)
- `GRABBER_ID` - optional operator-friendly identifier stored with telemetry (example: `pi-01`). Default: empty
- `RECONNECT_DELAY_SECONDS` - reconnect wait after disconnect/error. Default: `5`
- `SIMILARITY_THRESHOLD` - minimum SSIM score required to pass validation. Default: `0.80`
- `REFERENCE_IMAGE_1`, `REFERENCE_IMAGE_2`, `REFERENCE_IMAGE_3` - grayscale reference images used for SSIM comparison
- `FAILED_IMAGES_DIR` - local grabber directory where failed source frames are written before telemetry upload. Default: `failed_captures`

### Server

- `ADDR` - listen address. Default: `:8080`
- `GRABBER_PSK` - shared secret expected from the grabber WebSocket connection; **must exactly match grabber `GRABBER_PSK`**
- `GATE_SECRET` - secret required by `POST /api/gate/token`; **must exactly match gate-app `PUBLIC_GATE_SECRET`**
- `JWT_SECRET` - server-only HMAC secret for screenshot API tokens; do not expose in any client env
- `DB_PATH` - SQLite database path for validation logs. Default: `validation.db`
- `FAILED_IMAGES_DIR` - server-side storage path for failed images received through telemetry. Default: `failed-images`
- `ADMIN_PASSWORD_HASH` - Argon2id PHC hash used for admin login (hash only, never plaintext password). If empty, admin UI returns `503`
- `ADMIN_SESSION_SECRET` - server-only HMAC secret used to sign admin session cookies; independent from `JWT_SECRET`

A sample configuration file is provided at [`.env.example`](./.env.example).

## Secrets cheat sheet

| Variable | Used by | Must match with | Private/public |
| --- | --- | --- | --- |
| `GRABBER_PSK` | Grabber + Server | `GRABBER_PSK` on both components (same exact value) | **Private shared secret** |
| `GATE_SECRET` / `PUBLIC_GATE_SECRET` | Server + Gate app | Server `GATE_SECRET` == gate-app `PUBLIC_GATE_SECRET` | **Private shared secret** (treat as sensitive) |
| `JWT_SECRET` | Server only | Nothing else (do not reuse) | **Private server-only signing secret** |
| `ADMIN_PASSWORD_HASH` | Server only | Generated from your admin password (not plaintext) | Hash string (safe to store server-side; password remains private) |
| `ADMIN_SESSION_SECRET` | Server only | Nothing else; must be different from `JWT_SECRET` | **Private server-only session secret** |

### Secret generation quick commands

- Shared secrets (`GRABBER_PSK`, `GATE_SECRET`): **minimum 32 random bytes** (64 preferred for long-term rotation windows)
  - `openssl rand -base64 32`
  - `python3 -c "import secrets; print(secrets.token_urlsafe(32))"`
- Server-only signing/session secrets (`JWT_SECRET`, `ADMIN_SESSION_SECRET`): **64 random bytes preferred**
  - `openssl rand -base64 64`
  - `python3 -c "import secrets; print(secrets.token_urlsafe(64))"`
- Admin password hash (`ADMIN_PASSWORD_HASH`, Argon2id PHC):
  - `pip install argon2-cffi`
  - `python3 -c "from argon2 import PasswordHasher; print(PasswordHasher().hash('paste-password-from-password-manager'))"`

## Telemetry flow

Telemetry is sent over the **existing grabber WebSocket**; there is no separate telemetry HTTP endpoint.

For each capture request:

- the server generates a `request_id`
- the server sends `{"cmd":"capture","request_id":"..."}` to the grabber
- the grabber captures a frame and computes SSIM scores for each configured reference image
- on validation pass, the grabber sends the JPEG screenshot as a binary WebSocket frame
- on validation fail, the grabber sends a JSON WebSocket frame:

```json
{
  "type": "capture_result",
  "request_id": "4a8d0e5c0db54b3b8d6d1d1ef1dca123",
  "status": "validation_failed",
  "message": "Screenshot rejected by validator. A new capture will be requested automatically."
}
```

- the grabber then sends a JSON telemetry frame like:

```json
{
  "type": "telemetry",
  "request_id": "4a8d0e5c0db54b3b8d6d1d1ef1dca123",
  "timestamp": "2024-01-01T12:34:56Z",
  "grabber_id": "pi-01",
  "best_score": 0.9123,
  "scores": [0.9123, 0.7345, 0.8012],
  "threshold": 0.80,
  "decision": "pass",
  "failed_image_filename": null,
  "failed_image_data": null
}
```

If validation fails, the grabber includes:

- `failed_image_filename` like `2024-01-01/20240101_123456_abcd1234.jpg`
- `failed_image_data` containing the failed source frame as base64 JPEG

The server stores:

- telemetry metadata in SQLite `validation_logs`
- failed image bytes on disk under `FAILED_IMAGES_DIR`

## Failed image storage

### On the grabber

Failed source frames are saved under:

```text
FAILED_IMAGES_DIR/YYYY-MM-DD/YYYYMMDD_HHMMSS_shortid.jpg
```

Example:

```text
failed_captures/2024-01-01/20240101_123456_abcd1234.jpg
```

### On the server

When telemetry contains `failed_image_data`, the server decodes and saves the image under:

```text
FAILED_IMAGES_DIR/YYYY-MM-DD/YYYYMMDD_HHMMSS_shortid.jpg
```

The relative path is stored in SQLite as `failed_image_path` and linked from the admin UI.

## Retention policy

The server keeps validation data for **7 days**.

- Retention runs once at startup
- Then it runs every hour
- Old `validation_logs` rows are deleted from SQLite
- Associated failed images are removed from `FAILED_IMAGES_DIR`
- Old date directories are swept and removed when empty

Retention window constant: `7 * 24 * time.Hour`

## Admin UI setup

The admin UI is served from:

- `GET /admin/login`
- `POST /admin/login`
- `POST /admin/logout`
- `GET /admin/logs`
- `GET /admin/failed-images/<relative-path>`

### 1. Generate an Argon2id password hash

```bash
pip install argon2-cffi
python3 -c "from argon2 import PasswordHasher; print(PasswordHasher().hash('yourpassword'))"
```

Set the result as `ADMIN_PASSWORD_HASH`.

### 2. Generate a session signing secret

```bash
openssl rand -base64 64
```

Set the result as `ADMIN_SESSION_SECRET`.

### 3. Start the server with both values set

If `ADMIN_PASSWORD_HASH` is empty, the admin UI is intentionally disabled.

### Admin authentication details

- Password verification uses **Argon2id** PHC-format hashes
- Login creates an **HMAC-SHA256 signed** cookie named `admin_session`
- Session TTL is **12 hours**
- Login POST requests perform a basic Origin/Referer CSRF check
- Admin routes are served directly and are **not** part of the API CORS flow

## Admin UI features

The validation logs page includes:

- summary cards for total, pass, fail, and pass rate
- filters for time range and decision (`pass` / `fail`)
- latest-first ordering
- up to 500 filtered records per page load
- reference score display for each capture
- request ID preview and grabber ID display
- direct links to stored failed images for failed validations

## Database schema

Validation logs are stored in SQLite in the `validation_logs` table with:

- `created_at`
- `request_id`
- `best_score`
- `threshold`
- `decision`
- `scores_json`
- `failed_image_path`
- `grabber_id`

Indexes are created on `created_at` and `decision`.

## Running locally

### Server

```bash
cd server
go run .
```

### Grabber

```bash
python3 grabber/grabber.py
```

## Sample Caddy config

This example terminates TLS, forwards API and admin traffic to the Go server, and preserves secure cookie behavior through `X-Forwarded-Proto`:

```caddyfile
screens.example.com {
    encode gzip

    @api path /api/* /ws/* /admin/*
    reverse_proxy @api 127.0.0.1:8080 {
        header_up X-Forwarded-Proto {scheme}
        header_up Host {host}
    }
}
```

## Notes

- Screenshot responses are cached in memory on the server for 1 minute (`cacheTTL`)
- A validation `fail` returns JSON with `status=validation_failed` and a user-safe message, while preserving the original failed frame in telemetry
- Telemetry is best-effort; successful screenshot delivery still uses the binary WebSocket message first
