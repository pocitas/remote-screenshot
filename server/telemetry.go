package main

import (
	"encoding/json"
	"log"
	"time"
)

type telemetryMsg struct {
	Type                string    `json:"type"`
	RequestID           string    `json:"request_id"`
	Timestamp           string    `json:"timestamp"`
	GrabberID           string    `json:"grabber_id"`
	BestScore           float64   `json:"best_score"`
	Scores              []float64 `json:"scores"`
	Threshold           float64   `json:"threshold"`
	Decision            string    `json:"decision"`
	FailedImageFilename string    `json:"failed_image_filename"`
	FailedImageData     string    `json:"failed_image_data"`
}

func (s *serverState) handleTelemetryMessage(payload []byte) {
	var msg telemetryMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Printf("telemetry: parse error: %v", err)
		return
	}
	if msg.Type != "telemetry" {
		return
	}

	createdAt := time.Now().UTC()
	if msg.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			createdAt = t.UTC()
		}
	}

	var failedImagePath string
	if msg.FailedImageFilename != "" && msg.FailedImageData != "" {
		savedPath, err := s.saveFailedImage(msg.FailedImageFilename, msg.FailedImageData)
		if err != nil {
			log.Printf("telemetry: save failed image: %v", err)
		} else {
			failedImagePath = savedPath
		}
	}

	entry := validationLog{
		CreatedAt:       createdAt,
		RequestID:       msg.RequestID,
		BestScore:       msg.BestScore,
		Threshold:       msg.Threshold,
		Decision:        msg.Decision,
		Scores:          msg.Scores,
		FailedImagePath: failedImagePath,
		GrabberID:       msg.GrabberID,
	}

	if err := s.insertLog(entry); err != nil {
		log.Printf("telemetry: insert log: %v", err)
		return
	}
	log.Printf("telemetry: recorded request_id=%s decision=%s best_score=%.4f", msg.RequestID, msg.Decision, msg.BestScore)
}
