package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type serverState struct {
	grabberPSK string
	gateSecret string
	jwtSecret  []byte

	cacheTTL time.Duration

	mu             sync.Mutex
	grabberConn    *websocket.Conn
	lastCapture    captureResult
	lastCaptureAt  time.Time
	pendingCapture chan captureResult
	pendingReqID   string

	captureMu sync.Mutex
	wsWriteMu sync.Mutex

	referenceMu           sync.Mutex
	pendingReference      chan referenceResultMsg
	pendingReferenceReqID string

	db                 *sql.DB
	failedImagesDir    string
	adminPasswordHash  string
	adminSessionSecret []byte
}

type captureResult struct {
	Image             []byte
	ValidationFailure *validationFailureResponse
}

type validationFailureResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type captureResultMsg struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

type referenceResultMsg struct {
	Type        string   `json:"type"`
	RequestID   string   `json:"request_id"`
	Status      string   `json:"status"`
	Action      string   `json:"action"`
	Name        string   `json:"name"`
	Error       string   `json:"error"`
	References  []string `json:"references"`
	ImageBase64 string   `json:"image_base64"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

func main() {
	dbPath := envOrDefault("DB_PATH", "validation.db")
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	state := &serverState{
		grabberPSK:         envOrDefault("GRABBER_PSK", "change-me"),
		gateSecret:         envOrDefault("GATE_SECRET", "gate-secret"),
		jwtSecret:          []byte(envOrDefault("JWT_SECRET", "jwt-secret")),
		cacheTTL:           time.Minute,
		db:                 db,
		failedImagesDir:    envOrDefault("FAILED_IMAGES_DIR", "failed-images"),
		adminPasswordHash:  envOrDefault("ADMIN_PASSWORD_HASH", ""),
		adminSessionSecret: []byte(envOrDefault("ADMIN_SESSION_SECRET", "")),
	}

	if err := os.MkdirAll(state.failedImagesDir, 0o755); err != nil {
		log.Fatalf("mkdir failed images: %v", err)
	}

	state.startRetentionLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/grabber", state.handleGrabberWS)
	mux.HandleFunc("/api/gate/token", state.handleGateToken)
	mux.HandleFunc("/api/screenshot", state.handleScreenshot)
	mux.HandleFunc("/admin/login", state.handleAdminLogin)
	mux.HandleFunc("/admin/logout", state.handleAdminLogout)
	mux.HandleFunc("/admin/logs", state.handleAdminLogs)
	mux.HandleFunc("/admin/failed-images/", state.handleFailedImages)
	mux.HandleFunc("/admin/references", state.handleAdminReferences)
	mux.HandleFunc("/admin/references/capture", state.handleAdminReferenceCapture)
	mux.HandleFunc("/admin/references/delete", state.handleAdminReferenceDelete)
	mux.HandleFunc("/admin/references/add-failed", state.handleAdminReferenceAddFailed)
	mux.HandleFunc("/admin/references/image/", state.handleAdminReferenceImage)

	addr := envOrDefault("ADDR", ":8080")
	log.Printf("server listening on %s", addr)
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func (s *serverState) handleGrabberWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	psk := r.Header.Get("X-Grabber-PSK")
	if psk == "" {
		psk = r.URL.Query().Get("psk")
	}
	if psk != s.grabberPSK {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}

	s.mu.Lock()
	if s.grabberConn != nil {
		_ = s.grabberConn.Close()
	}
	s.grabberConn = conn
	s.mu.Unlock()

	log.Print("grabber connected")
	s.readGrabberLoop(conn)

	s.mu.Lock()
	if s.grabberConn == conn {
		s.grabberConn = nil
	}
	s.mu.Unlock()
	_ = conn.Close()
	log.Print("grabber disconnected")
}

func (s *serverState) readGrabberLoop(conn *websocket.Conn) {
	for {
		messageType, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}

		if messageType == websocket.BinaryMessage {
			s.mu.Lock()
			pending := s.pendingCapture
			s.mu.Unlock()
			if pending == nil {
				continue
			}
			select {
			case pending <- captureResult{Image: payload}:
			default:
			}
		} else if messageType == websocket.TextMessage {
			if s.handleCaptureResultMessage(payload) {
				continue
			}
			if s.handleReferenceResultMessage(payload) {
				continue
			}
			go s.handleTelemetryMessage(payload)
		}
	}
}

func (s *serverState) handleCaptureResultMessage(payload []byte) bool {
	var msg captureResultMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	if msg.Type != "capture_result" || msg.Status != "validation_failed" || msg.RequestID == "" {
		return false
	}

	s.mu.Lock()
	pending := s.pendingCapture
	pendingReqID := s.pendingReqID
	s.mu.Unlock()
	if pending == nil || pendingReqID != msg.RequestID {
		return true
	}

	response := captureResult{
		ValidationFailure: &validationFailureResponse{
			Status:  msg.Status,
			Message: strings.TrimSpace(msg.Message),
		},
	}
	if response.ValidationFailure.Message == "" {
		response.ValidationFailure.Message = "Screenshot rejected by validator."
	}

	select {
	case pending <- response:
	default:
	}
	return true
}

func (s *serverState) handleGateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("X-Gate-Secret") != s.gateSecret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	expiresAt := time.Now().UTC().Add(13 * time.Hour)
	claims := jwt.MapClaims{
		"exp": expiresAt.Unix(),
		"iat": time.Now().UTC().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"token":      signed,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (s *serverState) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tokenString, ok := extractBearerToken(r.Header.Get("Authorization"))
	if !ok || !s.validJWT(tokenString) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	result, err := s.getScreenshot(r.Context())
	if err != nil {
		if errors.Is(err, errNoGrabberConnection) {
			http.Error(w, "grabber is not connected", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to capture screenshot", http.StatusGatewayTimeout)
		return
	}

	if result.ValidationFailure != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result.ValidationFailure)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(result.Image)
}

var errNoGrabberConnection = errors.New("no grabber connection")

func (s *serverState) getScreenshot(ctx context.Context) (captureResult, error) {
	s.mu.Lock()
	if time.Since(s.lastCaptureAt) < s.cacheTTL && (len(s.lastCapture.Image) > 0 || s.lastCapture.ValidationFailure != nil) {
		cached := cloneCaptureResult(s.lastCapture)
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	s.captureMu.Lock()
	defer s.captureMu.Unlock()

	s.mu.Lock()
	if time.Since(s.lastCaptureAt) < s.cacheTTL && (len(s.lastCapture.Image) > 0 || s.lastCapture.ValidationFailure != nil) {
		cached := cloneCaptureResult(s.lastCapture)
		s.mu.Unlock()
		return cached, nil
	}

	conn := s.grabberConn
	if conn == nil {
		s.mu.Unlock()
		return captureResult{}, errNoGrabberConnection
	}
	reqID := generateRandomToken()

	response := make(chan captureResult, 1)
	s.pendingCapture = response
	s.pendingReqID = reqID
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.pendingCapture == response {
			s.pendingCapture = nil
			s.pendingReqID = ""
		}
		s.mu.Unlock()
	}()

	s.wsWriteMu.Lock()
	err := conn.WriteJSON(map[string]string{"cmd": "capture", "request_id": reqID})
	s.wsWriteMu.Unlock()
	if err != nil {
		return captureResult{}, err
	}

	select {
	case result := <-response:
		s.mu.Lock()
		s.lastCapture = cloneCaptureResult(result)
		s.lastCaptureAt = time.Now()
		s.mu.Unlock()
		return result, nil
	case <-ctx.Done():
		return captureResult{}, ctx.Err()
	case <-time.After(20 * time.Second):
		return captureResult{}, errors.New("timed out waiting for grabber")
	}
}

func cloneCaptureResult(result captureResult) captureResult {
	cloned := captureResult{
		Image: append([]byte(nil), result.Image...),
	}
	if result.ValidationFailure != nil {
		cloned.ValidationFailure = &validationFailureResponse{
			Status:  result.ValidationFailure.Status,
			Message: result.ValidationFailure.Message,
		}
	}
	return cloned
}

func (s *serverState) validJWT(tokenString string) bool {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return s.jwtSecret, nil
	})
	return err == nil && token.Valid
}

func extractBearerToken(header string) (string, bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Gate-Secret, X-Grabber-PSK")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
