package volley

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestEngineQueueGateAliasAffectsPending(t *testing.T) {
	e := NewEngine()
	req := &http.Request{}

	e.QueueGate("g1", req, nil)
	if got := e.Pending(); got != 1 {
		t.Fatalf("pending after QueueGate: got=%d want=1", got)
	}
}

func TestEngineOpenAliasesNoQueue(t *testing.T) {
	e := NewEngine()

	if err := e.OpenGate(context.Background(), "g1"); err != nil {
		t.Fatalf("OpenGate unexpected error: %v", err)
	}
	if err := e.OpenGateWithTimeout("g1", 50*time.Millisecond); err != nil {
		t.Fatalf("OpenGateWithTimeout unexpected error: %v", err)
	}
}

func TestEngineOpenWithTimeoutNoQueue(t *testing.T) {
	e := NewEngine()
	start := time.Now()
	if err := e.OpenWithTimeout(50 * time.Millisecond); err != nil {
		t.Fatalf("OpenWithTimeout unexpected error: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("OpenWithTimeout took too long with empty queue")
	}
}
