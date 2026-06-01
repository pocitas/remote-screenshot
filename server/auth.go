package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const sessionCookieName = "admin_session"
const sessionTTL = 12 * time.Hour

// verifyArgon2idHash verifies a password against a PHC-format Argon2id hash string.
// Format: $argon2id$v=19$m=<mem>,t=<time>,p=<par>$<b64salt>$<b64hash>
func verifyArgon2idHash(password, encodedHash string) error {
	parts := strings.Split(encodedHash, "$")
	// parts[0]="" parts[1]="argon2id" parts[2]="v=19" parts[3]="m=...,t=...,p=..." parts[4]=salt parts[5]=hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("invalid hash format")
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	var keyLen uint32

	for _, kv := range strings.Split(parts[3], ",") {
		pair := strings.SplitN(kv, "=", 2)
		if len(pair) != 2 {
			continue
		}
		v, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil {
			return fmt.Errorf("invalid param %q: %w", kv, err)
		}
		switch pair[0] {
		case "m":
			memory = uint32(v)
		case "t":
			iterations = uint32(v)
		case "p":
			parallelism = uint8(v)
		}
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}
	hashBytes, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("decode hash: %w", err)
	}
	keyLen = uint32(len(hashBytes))

	computed := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLen)
	if !hmac.Equal(computed, hashBytes) {
		return errors.New("password mismatch")
	}
	return nil
}

// createSessionCookie creates a signed session cookie value.
// Format: <hex_expiry_unix>.<hex_hmac>
func createSessionCookie(secret []byte) string {
	expiry := time.Now().UTC().Add(sessionTTL)
	expiryBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(expiryBytes, uint64(expiry.Unix()))

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(expiryBytes)
	sig := mac.Sum(nil)

	return hex.EncodeToString(expiryBytes) + "." + hex.EncodeToString(sig)
}

// validateSessionCookie checks the cookie value and returns true if valid and not expired.
func validateSessionCookie(value string, secret []byte) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expiryBytes, err := hex.DecodeString(parts[0])
	if err != nil || len(expiryBytes) != 8 {
		return false
	}
	sig, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(expiryBytes)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return false
	}

	expiry := int64(binary.BigEndian.Uint64(expiryBytes))
	return time.Now().UTC().Unix() < expiry
}

func isSecureRequest(r *http.Request) bool {
	return r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
}

func (s *serverState) setSessionCookie(w http.ResponseWriter, r *http.Request) {
	value := createSessionCookie(s.adminSessionSecret)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *serverState) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
		MaxAge:   -1,
	})
}

func (s *serverState) isAuthenticated(r *http.Request) bool {
	if len(s.adminSessionSecret) == 0 {
		return false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return validateSessionCookie(cookie.Value, s.adminSessionSecret)
}

// checkCSRF validates the Origin or Referer header for POST requests to prevent CSRF.
func checkCSRF(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		return strings.HasPrefix(origin, "https://"+r.Host) || strings.HasPrefix(origin, "http://"+r.Host)
	}
	referer := r.Header.Get("Referer")
	return strings.HasPrefix(referer, "https://"+r.Host+"/") || strings.HasPrefix(referer, "http://"+r.Host+"/")
}

// generateRandomToken generates a random hex token (for internal use if needed)
func generateRandomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
