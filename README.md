"# remote-screenshot" 

Initial scaffolding for a 4-part remote screenshot system:

- `grabber/` (Python): receives `{"cmd":"capture"}` on WebSocket, captures from default video device with OpenCV, validates captures with SSIM against 3 startup-loaded reference images, and sends JPEG bytes (or a placeholder when validation fails).
- `server/` (Go): authenticates grabber WebSocket with PSK, issues 13-hour JWTs from `POST /api/gate/token`, and serves `GET /api/screenshot` with 1-minute screenshot caching.
- `gate-app/` (SvelteKit + static adapter): requests a fresh token every 5 minutes and renders it as a QR code.
- `viewer-app/` (SvelteKit + static adapter): scans QR JWT, stores token, fetches screenshots every 1 minute, and clears auth on `401`."
