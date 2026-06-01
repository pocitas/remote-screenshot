package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const dbSchema = `
CREATE TABLE IF NOT EXISTS validation_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL,
    request_id TEXT NOT NULL,
    best_score REAL NOT NULL,
    threshold REAL NOT NULL,
    decision TEXT NOT NULL,
    scores_json TEXT NOT NULL,
    failed_image_path TEXT,
    grabber_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_vl_created_at ON validation_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_vl_decision ON validation_logs(decision);
`

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(dbSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}

type validationLog struct {
	ID              int64
	CreatedAt       time.Time
	RequestID       string
	BestScore       float64
	Threshold       float64
	Decision        string
	Scores          []float64
	FailedImagePath string
	GrabberID       string
}

func (s *serverState) insertLog(entry validationLog) error {
	scoresJSON, err := json.Marshal(entry.Scores)
	if err != nil {
		return err
	}
	var failedPath *string
	if entry.FailedImagePath != "" {
		failedPath = &entry.FailedImagePath
	}
	var grabberID *string
	if entry.GrabberID != "" {
		grabberID = &entry.GrabberID
	}
	_, err = s.db.Exec(
		`INSERT INTO validation_logs (created_at, request_id, best_score, threshold, decision, scores_json, failed_image_path, grabber_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.CreatedAt.UTC().Format(time.RFC3339),
		entry.RequestID,
		entry.BestScore,
		entry.Threshold,
		entry.Decision,
		string(scoresJSON),
		failedPath,
		grabberID,
	)
	return err
}

type logsFilter struct {
	From     time.Time
	To       time.Time
	Decision string
}

type logsSummary struct {
	Total     int
	PassCount int
	FailCount int
	PassRate  float64
}

func (s *serverState) queryLogs(f logsFilter) ([]validationLog, logsSummary, error) {
	query := `SELECT id, created_at, request_id, best_score, threshold, decision, scores_json, COALESCE(failed_image_path, ''), COALESCE(grabber_id, '') FROM validation_logs WHERE 1=1`
	args := []interface{}{}

	if !f.From.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, f.From.UTC().Format(time.RFC3339))
	}
	if !f.To.IsZero() {
		query += " AND created_at <= ?"
		args = append(args, f.To.UTC().Format(time.RFC3339))
	}
	if f.Decision == "pass" || f.Decision == "fail" {
		query += " AND decision = ?"
		args = append(args, f.Decision)
	}
	query += " ORDER BY created_at DESC LIMIT 500"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, logsSummary{}, err
	}
	defer rows.Close()

	var logs []validationLog
	for rows.Next() {
		var entry validationLog
		var createdAtStr, scoresJSON string
		if err := rows.Scan(&entry.ID, &createdAtStr, &entry.RequestID, &entry.BestScore, &entry.Threshold, &entry.Decision, &scoresJSON, &entry.FailedImagePath, &entry.GrabberID); err != nil {
			return nil, logsSummary{}, err
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		_ = json.Unmarshal([]byte(scoresJSON), &entry.Scores)
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, logsSummary{}, err
	}

	var summary logsSummary
	for _, l := range logs {
		summary.Total++
		if l.Decision == "pass" {
			summary.PassCount++
		} else {
			summary.FailCount++
		}
	}
	if summary.Total > 0 {
		summary.PassRate = float64(summary.PassCount) / float64(summary.Total) * 100
	}

	return logs, summary, nil
}

func (s *serverState) deleteOldLogs(before time.Time) ([]string, error) {
	cutoff := before.UTC().Format(time.RFC3339)
	rows, err := s.db.Query(
		`SELECT failed_image_path FROM validation_logs WHERE created_at < ? AND failed_image_path IS NOT NULL AND failed_image_path != ''`,
		cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			paths = append(paths, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	_, err = s.db.Exec(`DELETE FROM validation_logs WHERE created_at < ?`, cutoff)
	return paths, err
}
