package main

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

const retentionDuration = 7 * 24 * time.Hour

func (s *serverState) runRetention() {
	before := time.Now().UTC().Add(-retentionDuration)
	paths, err := s.deleteOldLogs(before)
	if err != nil {
		log.Printf("retention: delete old logs: %v", err)
	} else {
		log.Printf("retention: deleted logs older than %s", before.Format(time.RFC3339))
	}

	for _, relPath := range paths {
		fullPath := filepath.Join(s.failedImagesDir, relPath)
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			log.Printf("retention: remove image %s: %v", fullPath, err)
		}
	}
	s.cleanupOldImageDirs(before)
}

func (s *serverState) cleanupOldImageDirs(before time.Time) {
	entries, err := os.ReadDir(s.failedImagesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t, err := time.Parse("2006-01-02", entry.Name())
		if err != nil {
			continue
		}
		if t.Before(before.Add(-24 * time.Hour)) {
			dirPath := filepath.Join(s.failedImagesDir, entry.Name())
			_ = os.Remove(dirPath)
		}
	}
}

func (s *serverState) startRetentionLoop() {
	s.runRetention()
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			s.runRetention()
		}
	}()
}
