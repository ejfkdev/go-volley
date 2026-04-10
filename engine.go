package volley

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"
)

// Engine is a Turbo-Intruder-inspired batch orchestrator.
// One Engine instance represents one request queue group.
type Engine struct {
	Transport *Transport
	Client    *http.Client
	// H2SettleDelay is used only in HTTP/2 single-packet mode to allow multiple
	// streams to enqueue frames onto the shared connection before Fire().
	H2SettleDelay time.Duration

	queued int32
}

// NewEngine creates an engine with a default transport and client.
func NewEngine() *Engine {
	t := NewTransport()
	return &Engine{
		Transport:     t,
		Client:        &http.Client{Transport: t},
		H2SettleDelay: 50 * time.Millisecond,
	}
}

// Queue schedules a request in this engine's queue and executes it asynchronously.
// The request is sent immediately, but Transport keeps the final byte buffered until Open() fires.
func (e *Engine) Queue(req *http.Request, cb func(*http.Response, error)) {
	atomic.AddInt32(&e.queued, 1)

	go func() {
		resp, err := e.Client.Do(req)
		if cb != nil {
			cb(resp, err)
		}
	}()
}

// Open waits until queued requests are held, then fires them in a synchronized wave.
// Reset is called automatically after firing so the same engine can be reused for next batch.
func (e *Engine) Open(ctx context.Context) error {
	want := int(atomic.SwapInt32(&e.queued, 0))
	if want == 0 {
		return nil
	}
	if err := e.Transport.Wait(ctx, want); err != nil {
		return err
	}
	if e.Transport.enableHTTP2SinglePack && e.H2SettleDelay > 0 {
		timer := time.NewTimer(e.H2SettleDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	e.Transport.Fire()
	e.Transport.Reset()
	return nil
}

// OpenWithTimeout is a helper for non-context callers.
func (e *Engine) OpenWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return e.Open(ctx)
}

// Pending returns the current queued request count.
func (e *Engine) Pending() int {
	return int(atomic.LoadInt32(&e.queued))
}

// QueueGate is a backward-compatible alias. gate is ignored.
func (e *Engine) QueueGate(_ string, req *http.Request, cb func(*http.Response, error)) {
	e.Queue(req, cb)
}

// OpenGate is a backward-compatible alias. gate is ignored.
func (e *Engine) OpenGate(ctx context.Context, _ string) error {
	return e.Open(ctx)
}

// OpenGateWithTimeout is a backward-compatible alias. gate is ignored.
func (e *Engine) OpenGateWithTimeout(_ string, timeout time.Duration) error {
	return e.OpenWithTimeout(timeout)
}
