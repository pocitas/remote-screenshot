package main

import "testing"

func TestHandleCaptureResultMessageValidationFailed(t *testing.T) {
	state := &serverState{
		pendingCapture: make(chan captureResult, 1),
		pendingReqID:   "req-1",
	}

	handled := state.handleCaptureResultMessage([]byte(`{"type":"capture_result","request_id":"req-1","status":"validation_failed","message":"validator rejected frame"}`))
	if !handled {
		t.Fatalf("expected capture result message to be handled")
	}

	select {
	case result := <-state.pendingCapture:
		if result.ValidationFailure == nil {
			t.Fatalf("expected validation failure payload")
		}
		if result.ValidationFailure.Status != "validation_failed" {
			t.Fatalf("unexpected status: %s", result.ValidationFailure.Status)
		}
		if result.ValidationFailure.Message != "validator rejected frame" {
			t.Fatalf("unexpected message: %s", result.ValidationFailure.Message)
		}
	default:
		t.Fatalf("expected validation failure capture result to be queued")
	}
}

func TestHandleCaptureResultMessageInvalidPayload(t *testing.T) {
	state := &serverState{
		pendingCapture: make(chan captureResult, 1),
		pendingReqID:   "req-1",
	}

	handled := state.handleCaptureResultMessage([]byte(`{"type":"telemetry"}`))
	if handled {
		t.Fatalf("expected non capture_result payload to be ignored")
	}

	select {
	case <-state.pendingCapture:
		t.Fatalf("did not expect capture result to be queued")
	default:
	}
}

func TestHandleReferenceResultMessage(t *testing.T) {
	state := &serverState{
		pendingReference:      make(chan referenceResultMsg, 1),
		pendingReferenceReqID: "ref-1",
	}

	handled := state.handleReferenceResultMessage([]byte(`{"type":"reference_result","request_id":"ref-1","status":"ok","action":"list_references","references":["a.jpg","b.jpg"]}`))
	if !handled {
		t.Fatalf("expected reference result message to be handled")
	}

	select {
	case msg := <-state.pendingReference:
		if msg.Status != "ok" {
			t.Fatalf("unexpected status: %s", msg.Status)
		}
		if len(msg.References) != 2 {
			t.Fatalf("unexpected references count: %d", len(msg.References))
		}
	default:
		t.Fatalf("expected reference result to be queued")
	}
}

func TestHandleReferenceResultMessageInvalidPayload(t *testing.T) {
	state := &serverState{
		pendingReference:      make(chan referenceResultMsg, 1),
		pendingReferenceReqID: "ref-1",
	}

	handled := state.handleReferenceResultMessage([]byte(`{"type":"telemetry"}`))
	if handled {
		t.Fatalf("expected non reference_result payload to be ignored")
	}
}
