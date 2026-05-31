package main

import (
	"context"
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
	lastScreenshot []byte
	lastCaptureAt  time.Time
	pendingCapture chan []byte

	captureMu sync.Mutex
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

func main() {
	state := &serverState{
		grabberPSK: envOrDefault("GRABBER_PSK", "change-me"),
		gateSecret: envOrDefault("GATE_SECRET", "gate-secret"),
		jwtSecret:  []byte(envOrDefault("JWT_SECRET", "jwt-secret")),
		cacheTTL:   time.Minute,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/grabber", state.handleGrabberWS)
	mux.HandleFunc("/api/gate/token", state.handleGateToken)
	mux.HandleFunc("/api/screenshot", state.handleScreenshot)

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

		if messageType != websocket.BinaryMessage {
			continue
		}

		s.mu.Lock()
		pending := s.pendingCapture
		s.mu.Unlock()
		if pending == nil {
			continue
		}

		select {
		case pending <- payload:
		default:
		}
	}
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

	image, err := s.getScreenshot(r.Context())
	if err != nil {
		if errors.Is(err, errNoGrabberConnection) {
			http.Error(w, "grabber is not connected", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to capture screenshot", http.StatusGatewayTimeout)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(image)
}

var errNoGrabberConnection = errors.New("no grabber connection")

func (s *serverState) getScreenshot(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	if len(s.lastScreenshot) > 0 && time.Since(s.lastCaptureAt) < s.cacheTTL {
		cached := append([]byte(nil), s.lastScreenshot...)
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	s.captureMu.Lock()
	defer s.captureMu.Unlock()

	s.mu.Lock()
	if len(s.lastScreenshot) > 0 && time.Since(s.lastCaptureAt) < s.cacheTTL {
		cached := append([]byte(nil), s.lastScreenshot...)
		s.mu.Unlock()
		return cached, nil
	}

	conn := s.grabberConn
	if conn == nil {
		s.mu.Unlock()
		return nil, errNoGrabberConnection
	}

	response := make(chan []byte, 1)
	s.pendingCapture = response
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.pendingCapture == response {
			s.pendingCapture = nil
		}
		s.mu.Unlock()
	}()

	if err := conn.WriteJSON(map[string]string{"cmd": "capture"}); err != nil {
		return nil, err
	}

	select {
	case img := <-response:
		s.mu.Lock()
		s.lastScreenshot = append([]byte(nil), img...)
		s.lastCaptureAt = time.Now()
		s.mu.Unlock()
		return img, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		return nil, errors.New("timed out waiting for grabber")
	}
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
