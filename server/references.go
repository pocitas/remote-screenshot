package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var referencesTmpl = template.Must(template.New("references").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Reference Images</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,sans-serif;background:#f0f2f5;color:#1a1a2e}
header{background:#4f46e5;color:#fff;padding:1rem 1.5rem;display:flex;justify-content:space-between;align-items:center;gap:1rem}
header h1{font-size:1.2rem}
.header-left{display:flex;align-items:center;gap:1rem}
.nav a{color:#fff;text-decoration:none;background:rgba(255,255,255,.2);padding:.3rem .7rem;border-radius:4px;font-size:.85rem}
.nav a.active{background:rgba(255,255,255,.35)}
.logout{color:#fff;font-size:.875rem;text-decoration:none;background:rgba(255,255,255,.2);padding:.3rem .8rem;border-radius:4px;border:none;cursor:pointer}
.container{max-width:1100px;margin:1.5rem auto;padding:0 1rem}
.actions,.panel{background:#fff;border-radius:8px;box-shadow:0 1px 4px rgba(0,0,0,.08);padding:1rem 1.25rem}
.actions{margin-bottom:1rem}
button{background:#4f46e5;color:#fff;border:none;border-radius:4px;padding:.45rem .9rem;font-size:.85rem;cursor:pointer}
button:hover{background:#4338ca}
.error,.message{margin-bottom:1rem;padding:.6rem .8rem;border-radius:4px;font-size:.85rem}
.error{background:#fee2e2;color:#b91c1c}
.message{background:#dcfce7;color:#166534}
table{width:100%;border-collapse:collapse;font-size:.9rem}
th,td{padding:.6rem .4rem;border-bottom:1px solid #f1f3f5;text-align:left}
th{font-size:.78rem;text-transform:uppercase;color:#666}
.image-link{color:#4f46e5;text-decoration:none}
.image-link:hover{text-decoration:underline}
.muted{color:#666;font-size:.85rem}
</style>
</head>
<body>
<header>
  <div class="header-left">
    <h1>🖼️ Reference Images</h1>
    <nav class="nav">
      <a href="/admin/logs">Validation logs</a>
      <a class="active" href="/admin/references">References</a>
    </nav>
  </div>
  <form method="POST" action="/admin/logout" style="margin:0">
    <button class="logout" type="submit">Log out</button>
  </form>
</header>
<div class="container">
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  {{if .Message}}<div class="message">{{.Message}}</div>{{end}}
  <div class="actions">
    <form method="POST" action="/admin/references/capture">
      <button type="submit">Capture current frame as reference</button>
    </form>
  </div>
  <div class="panel">
    <table>
      <thead><tr><th>Name</th><th>Preview</th><th>Delete</th></tr></thead>
      <tbody>
      {{range .References}}
      <tr>
        <td>{{.}}</td>
        <td><a class="image-link" href="/admin/references/image/{{.}}" target="_blank">view</a></td>
        <td>
          <form method="POST" action="/admin/references/delete" style="margin:0">
            <input type="hidden" name="name" value="{{.}}">
            <button type="submit">Delete</button>
          </form>
        </td>
      </tr>
      {{else}}
      <tr><td colspan="3" class="muted">No reference images found.</td></tr>
      {{end}}
      </tbody>
    </table>
  </div>
</div>
</body>
</html>`))

func (s *serverState) handleReferenceResultMessage(payload []byte) bool {
	var msg referenceResultMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	if msg.Type != "reference_result" || msg.RequestID == "" {
		return false
	}

	s.mu.Lock()
	pending := s.pendingReference
	pendingReqID := s.pendingReferenceReqID
	s.mu.Unlock()
	if pending == nil || pendingReqID != msg.RequestID {
		return true
	}

	select {
	case pending <- msg:
	default:
	}
	return true
}

func (s *serverState) sendReferenceCommand(ctx context.Context, payload map[string]string) (referenceResultMsg, error) {
	s.referenceMu.Lock()
	defer s.referenceMu.Unlock()

	s.mu.Lock()
	conn := s.grabberConn
	if conn == nil {
		s.mu.Unlock()
		return referenceResultMsg{}, errNoGrabberConnection
	}
	reqID := generateRandomToken()
	payload["request_id"] = reqID
	response := make(chan referenceResultMsg, 1)
	s.pendingReference = response
	s.pendingReferenceReqID = reqID
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.pendingReference == response {
			s.pendingReference = nil
			s.pendingReferenceReqID = ""
		}
		s.mu.Unlock()
	}()

	s.wsWriteMu.Lock()
	err := conn.WriteJSON(payload)
	s.wsWriteMu.Unlock()
	if err != nil {
		return referenceResultMsg{}, err
	}

	select {
	case msg := <-response:
		if msg.Status != "ok" {
			message := strings.TrimSpace(msg.Error)
			if message == "" {
				message = "reference operation failed"
			}
			return referenceResultMsg{}, errors.New(message)
		}
		return msg, nil
	case <-ctx.Done():
		return referenceResultMsg{}, ctx.Err()
	case <-time.After(20 * time.Second):
		return referenceResultMsg{}, errors.New("timed out waiting for grabber")
	}
}

func (s *serverState) listReferenceImages(ctx context.Context) ([]string, error) {
	msg, err := s.sendReferenceCommand(ctx, map[string]string{"cmd": "list_references"})
	if err != nil {
		return nil, err
	}
	return msg.References, nil
}

func (s *serverState) captureReferenceImage(ctx context.Context) (string, error) {
	msg, err := s.sendReferenceCommand(ctx, map[string]string{"cmd": "capture_reference"})
	if err != nil {
		return "", err
	}
	return msg.Name, nil
}

func (s *serverState) deleteReferenceImage(ctx context.Context, name string) error {
	_, err := s.sendReferenceCommand(ctx, map[string]string{
		"cmd":  "delete_reference",
		"name": name,
	})
	return err
}

func (s *serverState) addReferenceImage(ctx context.Context, imageBytes []byte) (string, error) {
	msg, err := s.sendReferenceCommand(ctx, map[string]string{
		"cmd":          "add_reference_image",
		"image_base64": base64.StdEncoding.EncodeToString(imageBytes),
	})
	if err != nil {
		return "", err
	}
	return msg.Name, nil
}

func (s *serverState) getReferenceImage(ctx context.Context, name string) ([]byte, error) {
	msg, err := s.sendReferenceCommand(ctx, map[string]string{
		"cmd":  "get_reference",
		"name": name,
	})
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(msg.ImageBase64)
}

func (s *serverState) handleAdminReferences(w http.ResponseWriter, r *http.Request) {
	if s.adminPasswordHash == "" {
		http.Error(w, "admin UI not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.isAuthenticated(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	references, err := s.listReferenceImages(r.Context())
	if err != nil {
		log.Printf("list references: %v", err)
		http.Error(w, "failed to list references", http.StatusBadGateway)
		return
	}

	data := map[string]interface{}{
		"References": references,
		"Message":    r.URL.Query().Get("msg"),
		"Error":      r.URL.Query().Get("err"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := referencesTmpl.Execute(w, data); err != nil {
		log.Printf("render references template: %v", err)
	}
}

func (s *serverState) handleAdminReferenceCapture(w http.ResponseWriter, r *http.Request) {
	if !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name, err := s.captureReferenceImage(r.Context())
	if err != nil {
		http.Redirect(w, r, "/admin/references?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/references?msg="+url.QueryEscape("Captured "+name), http.StatusSeeOther)
}

func (s *serverState) handleAdminReferenceDelete(w http.ResponseWriter, r *http.Request) {
	if !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/references?err="+url.QueryEscape("missing reference name"), http.StatusSeeOther)
		return
	}
	if err := s.deleteReferenceImage(r.Context(), name); err != nil {
		http.Redirect(w, r, "/admin/references?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/references?msg="+url.QueryEscape("Deleted "+name), http.StatusSeeOther)
}

func (s *serverState) handleAdminReferenceImage(w http.ResponseWriter, r *http.Request) {
	if !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/admin/references/image/")
	if name == "" || strings.Contains(name, "/") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	imageBytes, err := s.getReferenceImage(r.Context(), name)
	if err != nil {
		http.Error(w, "failed to fetch reference image", http.StatusBadGateway)
		return
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(imageBytes)
}

func (s *serverState) handleAdminReferenceAddFailed(w http.ResponseWriter, r *http.Request) {
	if !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkCSRF(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	failedImagePath := strings.TrimSpace(r.FormValue("failed_image_path"))
	if failedImagePath == "" {
		http.Redirect(w, r, "/admin/logs", http.StatusSeeOther)
		return
	}

	fullPath, err := resolveUnderBaseDir(s.failedImagesDir, failedImagePath)
	if err != nil {
		http.Redirect(w, r, "/admin/logs?err="+url.QueryEscape("invalid failed image path"), http.StatusSeeOther)
		return
	}
	imageBytes, err := os.ReadFile(fullPath)
	if err != nil {
		http.Redirect(w, r, "/admin/logs?err="+url.QueryEscape("failed to read image"), http.StatusSeeOther)
		return
	}
	name, err := s.addReferenceImage(r.Context(), imageBytes)
	if err != nil {
		http.Redirect(w, r, "/admin/logs?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/logs?msg="+url.QueryEscape("Added reference "+name), http.StatusSeeOther)
}
