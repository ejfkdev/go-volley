package volley

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Transport is a custom http.RoundTripper that implements the "Header Straddling" technique.
// It holds the last byte of the request body (or header) until Fire() is called.
type Transport struct {
	// Embedding http.Transport allows users to configure Proxy, TLS, etc.
	*http.Transport

	// --- Atomic Counters (Aliged at top for 32-bit compatibility) ---

	// dialStartCount tracks the number of dial attempts started.
	dialStartCount int32
	// dialInflight tracks the number of dials currently handshaking.
	dialInflight int32
	// aliveCount tracks the number of successfully established connections.
	aliveCount int32
	// heldCount tracks the number of connections that have buffered data and are ready to fire.
	heldCount int32
	// fired indicates whether the "Fire" signal has been triggered (0: Holding, 1: Fired).
	fired int32

	// heldConns keeps currently held/straddled connections for centralized fire fan-out.
	heldMu    sync.Mutex
	heldConns map[*StraddleConn]struct{}

	// notifyCh is used to wake up WaitHeldCount when counters change.
	notifyCh chan struct{}

	enableHTTP2           bool
	enableHTTP2SinglePack bool
}

// TransportOptions configures protocol behavior for Transport.
type TransportOptions struct {
	// EnableHTTP2 enables ALPN negotiation for HTTP/2 while keeping straddling semantics.
	EnableHTTP2 bool
	// EnableHTTP2SinglePacket enables experimental HTTP/2 single-packet style gating.
	// It forces a single shared HTTP/2 connection so multiple streams can be held behind
	// one final-byte release point.
	EnableHTTP2SinglePacket bool
}

// NewTransport creates a new Transport ready for race condition testing.
func NewTransport() *Transport {
	return NewTransportWithOptions(TransportOptions{})
}

// NewTransportWithOptions creates a transport with protocol options.
func NewTransportWithOptions(opts TransportOptions) *Transport {
	t := &Transport{
		notifyCh:              make(chan struct{}, 1),
		heldConns:             make(map[*StraddleConn]struct{}),
		enableHTTP2:           opts.EnableHTTP2,
		enableHTTP2SinglePack: opts.EnableHTTP2 && opts.EnableHTTP2SinglePacket,
	}

	// Helper to track dial state
	trackDial := func(dialFunc func() (net.Conn, error)) (net.Conn, error) {
		// 1. Mark attempt started
		atomic.AddInt32(&t.dialStartCount, 1)
		// 2. Mark inflight
		atomic.AddInt32(&t.dialInflight, 1)

		conn, err := dialFunc()

		// 3. Cleanup inflight status
		atomic.AddInt32(&t.dialInflight, -1)

		if err != nil {
			// Notify waiters that an inflight dial finished (failed)
			t.tryNotify()
			return nil, err
		}

		// 4. Wrap successful connection
		// Notify logic is handled inside wrapConn -> Close
		return t.wrapConn(conn), nil
	}

	// Initialize underlying http.Transport
	disableKeepAlives := true
	maxIdleConnsPerHost := -1
	maxConnsPerHost := 0
	if t.enableHTTP2SinglePack {
		disableKeepAlives = false
		maxIdleConnsPerHost = 1
		maxConnsPerHost = 1
	}

	t.Transport = &http.Transport{
		ForceAttemptHTTP2:   opts.EnableHTTP2,
		DisableKeepAlives:   disableKeepAlives,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		MaxConnsPerHost:     maxConnsPerHost,

		TLSClientConfig: &tls.Config{
			NextProtos:         nextProtos(opts.EnableHTTP2),
			InsecureSkipVerify: true, // Default to insecure for security testing tools
		},

		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Fast path: if already fired, bypass all tracking logic for performance
			if atomic.LoadInt32(&t.fired) == 1 {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}

			return trackDial(func() (net.Conn, error) {
				// We enforce a timeout on the handshake itself to prevent stuck "inflight" counters
				handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()

				var d net.Dialer
				rawConn, err := d.DialContext(handshakeCtx, network, addr)
				if err != nil {
					return nil, err
				}

				// Standard TLS setup
				tlsConfig := t.Transport.TLSClientConfig.Clone()
				if tlsConfig.ServerName == "" {
					if colon := strings.LastIndex(addr, ":"); colon > 0 {
						tlsConfig.ServerName = addr[:colon]
					} else {
						tlsConfig.ServerName = addr
					}
				}

				tlsConn := tls.Client(rawConn, tlsConfig)
				if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
					rawConn.Close()
					return nil, err
				}
				return tlsConn, nil
			})
		},

		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if atomic.LoadInt32(&t.fired) == 1 {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}

			return trackDial(func() (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			})
		},
	}
	return t
}

func nextProtos(enableHTTP2 bool) []string {
	if enableHTTP2 {
		return []string{"h2", "http/1.1"}
	}
	return []string{"http/1.1"}
}

// wrapConn encapsulates a net.Conn with straddling logic.
func (t *Transport) wrapConn(c net.Conn) *StraddleConn {
	atomic.AddInt32(&t.aliveCount, 1)

	// Notify WaitHeldCount that we have a new alive connection (wait condition might be met)
	t.tryNotify()

	return &StraddleConn{
		Conn:  c,
		owner: t,
	}
}

// Fire releases the last byte for all currently buffered connections.
// It also sets the transport to "Fired" mode, where subsequent requests pass through immediately.
func (t *Transport) Fire() {
	// CAS ensures we only fire once.
	if !atomic.CompareAndSwapInt32(&t.fired, 0, 1) {
		return
	}

	// Centralized fan-out in one goroutine to reduce scheduler jitter.
	conns := t.snapshotHeldConns()
	for _, sc := range conns {
		sc.release()
	}
}

// Reset clears the transport state, allowing it to be reused for a new batch of requests.
// NOTE: This must be called serially (not concurrently with Fire or WaitHeldCount).
func (t *Transport) Reset() {
	t.heldMu.Lock()
	t.heldConns = make(map[*StraddleConn]struct{})
	t.heldMu.Unlock()

	atomic.StoreInt32(&t.heldCount, 0)
	atomic.StoreInt32(&t.aliveCount, 0)
	atomic.StoreInt32(&t.dialStartCount, 0)
	atomic.StoreInt32(&t.dialInflight, 0)
	atomic.StoreInt32(&t.fired, 0)
}

// Wait blocks until the connection pool reaches the target state.
// Return conditions:
// 1. All initiated dials have finished (success or fail).
// 2. AND (Held connections >= want OR Held connections == Alive connections).
//
// This logic prevents hanging if some connections fail to establish.
func (t *Transport) Wait(ctx context.Context, want int) error {
	if want <= 0 {
		return nil
	}

	check := func() bool {
		start := atomic.LoadInt32(&t.dialStartCount)
		inflight := atomic.LoadInt32(&t.dialInflight)
		held := atomic.LoadInt32(&t.heldCount)
		alive := atomic.LoadInt32(&t.aliveCount)

		// Condition 1: Wait until all expected goroutines have started dialing.
		// In HTTP/2 single-packet mode, many requests may share one dial.
		if !t.enableHTTP2SinglePack {
			if start < int32(want) {
				return false
			}
		} else if start == 0 {
			return false
		}

		// Condition 2: Wait until all handshakes settle (no ramp-up)
		if inflight > 0 {
			return false
		}

		// Condition 3: Success logic
		// If alive is 0, it means all attempts failed. We should return to let caller handle errors.
		if alive == 0 {
			return true
		}
		// Otherwise, wait until all survivors are buffered/ready.
		return held == alive
	}

	// Fast path check
	if check() {
		return nil
	}

	// Slow path wait
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: timeout. want=%d, start=%d, inflight=%d, alive=%d, held=%d",
				ctx.Err(), want,
				atomic.LoadInt32(&t.dialStartCount),
				atomic.LoadInt32(&t.dialInflight),
				atomic.LoadInt32(&t.aliveCount),
				atomic.LoadInt32(&t.heldCount))

		case <-t.notifyCh:
			if check() {
				return nil
			}
		}
	}
}

func (t *Transport) tryNotify() {
	// Non-blocking send
	select {
	case t.notifyCh <- struct{}{}:
	default:
	}
}

func (t *Transport) registerHeldConn(sc *StraddleConn) {
	t.heldMu.Lock()
	t.heldConns[sc] = struct{}{}
	t.heldMu.Unlock()
}

func (t *Transport) unregisterHeldConn(sc *StraddleConn) {
	t.heldMu.Lock()
	delete(t.heldConns, sc)
	t.heldMu.Unlock()
}

func (t *Transport) snapshotHeldConns() []*StraddleConn {
	t.heldMu.Lock()
	conns := make([]*StraddleConn, 0, len(t.heldConns))
	for sc := range t.heldConns {
		conns = append(conns, sc)
	}
	t.heldConns = make(map[*StraddleConn]struct{})
	t.heldMu.Unlock()
	return conns
}

// --- Straddle Conn ---

type StraddleConn struct {
	net.Conn
	owner *Transport

	held      byte
	hasHeld   bool
	isCounted bool
	byteBuf   [1]byte

	// mu protects internal state of this specific connection only.
	// No global lock contention.
	mu sync.Mutex
}

func (sc *StraddleConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	// Hot Path: If already fired, bypass lock and buffering
	if atomic.LoadInt32(&sc.owner.fired) == 1 {
		return sc.Conn.Write(b)
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Double Check
	if atomic.LoadInt32(&sc.owner.fired) == 1 {
		if sc.hasHeld {
			_, _ = sc.writeHeldByteLocked()
			sc.hasHeld = false
		}
		return sc.Conn.Write(b)
	}

	if sc.hasHeld {
		if _, err := sc.writeHeldByteLocked(); err != nil {
			return 0, err
		}
	}

	// Keep the last byte
	sc.held = b[len(b)-1]
	sc.hasHeld = true

	// If this is the first time we hold data, increment counters and register conn
	if !sc.isCounted {
		atomic.AddInt32(&sc.owner.heldCount, 1)
		sc.owner.registerHeldConn(sc)
		sc.isCounted = true
		sc.owner.tryNotify()
	}

	// Send N-1 bytes
	if toSendLen := len(b) - 1; toSendLen > 0 {
		_, err := sc.Conn.Write(b[:toSendLen])
		if err != nil {
			return 0, err
		}
	}

	return len(b), nil
}

func (sc *StraddleConn) release() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.hasHeld {
		_, _ = sc.writeHeldByteLocked()
		sc.hasHeld = false
	}
}

func (sc *StraddleConn) writeHeldByteLocked() (int, error) {
	sc.byteBuf[0] = sc.held
	return sc.Conn.Write(sc.byteBuf[:])
}

func (sc *StraddleConn) Close() error {
	sc.mu.Lock()

	// If the connection was counted as "Held", we need to reverse that
	// if it closes before firing.
	if sc.isCounted {
		sc.owner.unregisterHeldConn(sc)

		// Only decrement if we haven't fired yet.
		// If we fired, the counter is conceptually "consumed".
		if atomic.LoadInt32(&sc.owner.fired) == 0 {
			atomic.AddInt32(&sc.owner.heldCount, -1)
		}
		sc.isCounted = false

		// Notify transport state change
		sc.owner.tryNotify()
	}

	// Flush before closing (best effort)
	if sc.hasHeld {
		sc.writeHeldByteLocked()
		sc.hasHeld = false
	}
	sc.mu.Unlock()

	// Decrement alive count
	atomic.AddInt32(&sc.owner.aliveCount, -1)
	sc.owner.tryNotify()

	return sc.Conn.Close()
}
