package main

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var loginTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Admin Login</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,sans-serif;background:#f0f2f5;display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#fff;border-radius:8px;box-shadow:0 2px 8px rgba(0,0,0,.15);padding:2rem;width:100%;max-width:360px}
h1{font-size:1.4rem;margin-bottom:1.5rem;color:#1a1a2e}
label{display:block;font-size:.875rem;font-weight:500;margin-bottom:.4rem;color:#555}
input[type=password]{width:100%;padding:.6rem .8rem;border:1px solid #ddd;border-radius:4px;font-size:1rem;margin-bottom:1rem}
input[type=password]:focus{outline:none;border-color:#4f46e5}
button{width:100%;padding:.7rem;background:#4f46e5;color:#fff;border:none;border-radius:4px;font-size:1rem;cursor:pointer}
button:hover{background:#4338ca}
.error{margin-top:1rem;color:#dc2626;font-size:.875rem}
</style>
</head>
<body>
<div class="card">
<h1>🔒 Admin Login</h1>
<form method="POST" action="/admin/login">
<label for="password">Password</label>
<input type="password" id="password" name="password" autofocus autocomplete="current-password">
<button type="submit">Log in</button>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
</form>
</div>
</body>
</html>`))

var logsTmpl = template.Must(template.New("logs").Funcs(template.FuncMap{
	"formatTime":  func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") },
	"formatScore": func(f float64) string { return fmt.Sprintf("%.4f", f) },
	"formatRate":  func(f float64) string { return fmt.Sprintf("%.1f%%", f) },
	"decisionClass": func(d string) string {
		if d == "pass" {
			return "pass"
		}
		return "fail"
	},
	"scoresStr": func(scores []float64) string {
		parts := make([]string, len(scores))
		for i, s := range scores {
			parts[i] = fmt.Sprintf("%.4f", s)
		}
		return strings.Join(parts, ", ")
	},
	"slice": func(s string, i, j int) string {
		if j > len(s) {
			j = len(s)
		}
		if i > len(s) {
			i = len(s)
		}
		return s[i:j]
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Validation Logs</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,sans-serif;background:#f0f2f5;color:#1a1a2e}
header{background:#4f46e5;color:#fff;padding:1rem 1.5rem;display:flex;justify-content:space-between;align-items:center}
header h1{font-size:1.2rem}
.logout{color:#fff;font-size:.875rem;text-decoration:none;background:rgba(255,255,255,.2);padding:.3rem .8rem;border-radius:4px;border:none;cursor:pointer}
.container{max-width:1400px;margin:1.5rem auto;padding:0 1rem}
.filters{background:#fff;border-radius:8px;padding:1rem 1.5rem;margin-bottom:1rem;box-shadow:0 1px 4px rgba(0,0,0,.08)}
.filters form{display:flex;flex-wrap:wrap;gap:.75rem;align-items:flex-end}
.filters label{font-size:.8rem;font-weight:500;color:#555}
.filters input,.filters select{border:1px solid #ddd;border-radius:4px;padding:.4rem .6rem;font-size:.875rem}
.filters button{background:#4f46e5;color:#fff;border:none;border-radius:4px;padding:.45rem 1.2rem;font-size:.875rem;cursor:pointer}
.filters button:hover{background:#4338ca}
.summary{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:1rem;margin-bottom:1rem}
.summary-card{background:#fff;border-radius:8px;padding:1rem 1.25rem;box-shadow:0 1px 4px rgba(0,0,0,.08);text-align:center}
.summary-card .val{font-size:1.8rem;font-weight:700;color:#4f46e5}
.summary-card .lbl{font-size:.8rem;color:#666;margin-top:.2rem}
.table-wrap{background:#fff;border-radius:8px;box-shadow:0 1px 4px rgba(0,0,0,.08);overflow-x:auto}
table{width:100%;border-collapse:collapse;font-size:.85rem}
th{background:#f8f9fa;padding:.65rem 1rem;text-align:left;font-size:.75rem;font-weight:600;color:#666;text-transform:uppercase;letter-spacing:.04em;border-bottom:1px solid #e9ecef}
td{padding:.6rem 1rem;border-bottom:1px solid #f1f3f5;vertical-align:middle}
tr:last-child td{border-bottom:none}
tr:hover td{background:#fafbfc}
.badge{display:inline-block;padding:.2rem .55rem;border-radius:99px;font-size:.75rem;font-weight:600}
.badge.pass{background:#dcfce7;color:#16a34a}
.badge.fail{background:#fee2e2;color:#dc2626}
.img-link{color:#4f46e5;text-decoration:none;font-size:.75rem}
.img-link:hover{text-decoration:underline}
.mono{font-family:monospace;font-size:.8rem}
</style>
</head>
<body>
<header>
  <h1>📊 Validation Logs</h1>
  <form method="POST" action="/admin/logout" style="margin:0">
    <button class="logout" type="submit">Log out</button>
  </form>
</header>
<div class="container">
  <div class="filters">
    <form method="GET" action="/admin/logs">
      <div><label>From<br><input type="datetime-local" name="from" value="{{.FilterFrom}}"></label></div>
      <div><label>To<br><input type="datetime-local" name="to" value="{{.FilterTo}}"></label></div>
      <div><label>Decision<br>
        <select name="decision">
          <option value="" {{if eq .FilterDecision ""}}selected{{end}}>All</option>
          <option value="pass" {{if eq .FilterDecision "pass"}}selected{{end}}>Pass</option>
          <option value="fail" {{if eq .FilterDecision "fail"}}selected{{end}}>Fail</option>
        </select>
      </label></div>
      <div><button type="submit">Filter</button></div>
    </form>
  </div>
  <div class="summary">
    <div class="summary-card"><div class="val">{{.Summary.Total}}</div><div class="lbl">Total</div></div>
    <div class="summary-card"><div class="val">{{.Summary.PassCount}}</div><div class="lbl">Pass</div></div>
    <div class="summary-card"><div class="val">{{.Summary.FailCount}}</div><div class="lbl">Fail</div></div>
    <div class="summary-card"><div class="val">{{formatRate .Summary.PassRate}}</div><div class="lbl">Pass Rate</div></div>
  </div>
  <div class="table-wrap">
    <table>
      <thead><tr>
        <th>Time (UTC)</th>
        <th>Decision</th>
        <th>Best Score</th>
        <th>Threshold</th>
        <th>Ref Scores</th>
        <th>Request ID</th>
        <th>Grabber</th>
        <th>Failed Image</th>
      </tr></thead>
      <tbody>
      {{range .Logs}}
      <tr>
        <td class="mono">{{formatTime .CreatedAt}}</td>
        <td><span class="badge {{decisionClass .Decision}}">{{.Decision}}</span></td>
        <td class="mono">{{formatScore .BestScore}}</td>
        <td class="mono">{{formatScore .Threshold}}</td>
        <td class="mono">{{scoresStr .Scores}}</td>
        <td class="mono" title="{{.RequestID}}">{{slice .RequestID 0 8}}…</td>
        <td>{{.GrabberID}}</td>
        <td>{{if .FailedImagePath}}<a class="img-link" href="/admin/failed-images/{{.FailedImagePath}}" target="_blank">view</a>{{else}}—{{end}}</td>
      </tr>
      {{else}}<tr><td colspan="8" style="text-align:center;padding:2rem;color:#999">No log entries found.</td></tr>{{end}}
      </tbody>
    </table>
  </div>
</div>
</body>
</html>`))

func (s *serverState) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminPasswordHash == "" {
		http.Error(w, "admin UI not configured", http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodGet {
		_ = loginTmpl.Execute(w, map[string]interface{}{"Error": ""})
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
	password := r.FormValue("password")
	if err := verifyArgon2idHash(password, s.adminPasswordHash); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = loginTmpl.Execute(w, map[string]interface{}{"Error": "Invalid password."})
		return
	}

	s.setSessionCookie(w, r)
	http.Redirect(w, r, "/admin/logs", http.StatusSeeOther)
}

func (s *serverState) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *serverState) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	if s.adminPasswordHash == "" {
		http.Error(w, "admin UI not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.isAuthenticated(r) {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	decision := r.URL.Query().Get("decision")

	var f logsFilter
	f.Decision = decision

	if fromStr != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", fromStr, time.UTC); err == nil {
			f.From = t
		}
	}
	if toStr != "" {
		if t, err := time.ParseInLocation("2006-01-02T15:04", toStr, time.UTC); err == nil {
			f.To = t
		}
	}

	logs, summary, err := s.queryLogs(f)
	if err != nil {
		log.Printf("query logs: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Logs":           logs,
		"Summary":        summary,
		"FilterFrom":     fromStr,
		"FilterTo":       toStr,
		"FilterDecision": decision,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := logsTmpl.Execute(w, data); err != nil {
		log.Printf("render logs template: %v", err)
	}
}

func (s *serverState) handleFailedImages(w http.ResponseWriter, r *http.Request) {
	if !s.isAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	relPath := strings.TrimPrefix(r.URL.Path, "/admin/failed-images/")
	fullPath, err := resolveUnderBaseDir(s.failedImagesDir, relPath)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.ServeFile(w, r, fullPath)
}

func (s *serverState) saveFailedImage(filename string, dataBase64 string) (string, error) {
	imgBytes, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	fullPath, err := resolveUnderBaseDir(s.failedImagesDir, filename)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(fullPath, imgBytes, 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return filepath.ToSlash(filepath.Clean(filename)), nil
}

func resolveUnderBaseDir(baseDir, relativePath string) (string, error) {
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("invalid path")
	}

	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("abs base: %w", err)
	}
	fullPath := filepath.Join(baseAbs, cleaned)
	fullAbs, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("abs full path: %w", err)
	}
	rel, err := filepath.Rel(baseAbs, fullAbs)
	if err != nil {
		return "", fmt.Errorf("rel path: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid path")
	}
	return fullAbs, nil
}
