package volley

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// H2SinglePacketEngine is a low-level HTTP/2 gate engine inspired by Turbo Intruder's
// single-packet technique. It writes all request frames except the final byte of each
// stream payload, then releases all held bytes in one burst via Fire().
//
// This engine bypasses net/http to control HTTP/2 framing directly.
type H2SinglePacketEngine struct {
	Addr      string
	TLSConfig *tls.Config

	mu             sync.Mutex
	conn           net.Conn
	bw             *bufio.Writer
	fr             *http2.Framer
	nextStream     uint32
	held           []byte
	queued         int
	singleByteGate [1]byte
}

// NewH2SinglePacketEngine creates an engine for host:port.
func NewH2SinglePacketEngine(addr string, tlsCfg *tls.Config) *H2SinglePacketEngine {
	cfg := tlsCfg
	if cfg == nil {
		cfg = &tls.Config{}
	}
	cloned := cfg.Clone()
	if len(cloned.NextProtos) == 0 {
		cloned.NextProtos = []string{"h2"}
	}
	return &H2SinglePacketEngine{
		Addr:           addr,
		TLSConfig:      cloned,
		nextStream:     1,
		singleByteGate: [1]byte{'X'},
	}
}

// Connect establishes TLS+h2 and sends client preface/settings.
func (e *H2SinglePacketEngine) Connect(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn != nil {
		return nil
	}
	d := &net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", e.Addr)
	if err != nil {
		return err
	}
	tlsConn := tls.Client(raw, e.TLSConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return err
	}
	state := tlsConn.ConnectionState()
	if state.NegotiatedProtocol != "h2" {
		tlsConn.Close()
		return fmt.Errorf("negotiated protocol is %q, want h2", state.NegotiatedProtocol)
	}

	bw := bufio.NewWriterSize(tlsConn, 64*1024)
	fr := http2.NewFramer(bw, tlsConn)
	if _, err := bw.WriteString(http2.ClientPreface); err != nil {
		tlsConn.Close()
		return err
	}
	if err := fr.WriteSettings(); err != nil {
		tlsConn.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		tlsConn.Close()
		return err
	}

	e.conn = tlsConn
	e.bw = bw
	e.fr = fr
	return nil
}

// QueueRequest writes HEADERS and DATA frames while withholding the final data byte.
// If req.Body is empty, a synthetic single-byte DATA frame is used as the gate.
func (e *H2SinglePacketEngine) QueueRequest(req *http.Request, body []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn == nil {
		return fmt.Errorf("engine not connected")
	}
	if req == nil {
		return fmt.Errorf("nil request")
	}
	streamID := e.nextStream
	e.nextStream += 2

	hdrBlock, err := encodeH2Headers(req)
	if err != nil {
		return err
	}
	if err := e.fr.WriteHeaders(http2.HeadersFrameParam{
		StreamID:      streamID,
		BlockFragment: hdrBlock,
		EndHeaders:    true,
		EndStream:     false,
	}); err != nil {
		return err
	}

	payload := body
	if len(payload) == 0 {
		payload = e.singleByteGate[:]
	}
	if err := writeDataFrameSplit(e.bw, streamID, payload); err != nil {
		return err
	}
	e.held = append(e.held, payload[len(payload)-1])
	e.queued++
	return nil
}

// Fire flushes all held last bytes in one burst.
func (e *H2SinglePacketEngine) Fire() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn == nil {
		return fmt.Errorf("engine not connected")
	}
	if len(e.held) == 0 {
		return nil
	}
	if _, err := e.bw.Write(e.held); err != nil {
		return err
	}
	e.held = e.held[:0]
	e.queued = 0
	return e.bw.Flush()
}

// ResetGate clears queued state while keeping connection alive.
func (e *H2SinglePacketEngine) ResetGate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.held = e.held[:0]
	e.queued = 0
}

// Close closes underlying connection.
func (e *H2SinglePacketEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.conn == nil {
		return nil
	}
	err := e.conn.Close()
	e.conn = nil
	e.bw = nil
	e.fr = nil
	return err
}

func encodeH2Headers(req *http.Request) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if req.URL == nil {
		return nil, fmt.Errorf("nil request URL")
	}
	authority := req.Host
	if authority == "" {
		authority = req.URL.Host
	}
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	type headerKV struct {
		lower string
		key   string
	}
	kvs := make([]headerKV, 0, len(req.Header))
	for k := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || strings.HasPrefix(lk, ":") {
			continue
		}
		kvs = append(kvs, headerKV{lower: lk, key: k})
	}
	if len(kvs) > 1 {
		sort.Slice(kvs, func(i, j int) bool {
			if kvs[i].lower == kvs[j].lower {
				return kvs[i].key < kvs[j].key
			}
			return kvs[i].lower < kvs[j].lower
		})
	}

	var b bytes.Buffer
	enc := hpack.NewEncoder(&b)
	if err := enc.WriteField(hpack.HeaderField{Name: ":method", Value: method}); err != nil {
		return nil, err
	}
	scheme := req.URL.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if err := enc.WriteField(hpack.HeaderField{Name: ":scheme", Value: scheme}); err != nil {
		return nil, err
	}
	if err := enc.WriteField(hpack.HeaderField{Name: ":authority", Value: authority}); err != nil {
		return nil, err
	}
	if err := enc.WriteField(hpack.HeaderField{Name: ":path", Value: path}); err != nil {
		return nil, err
	}
	for _, kv := range kvs {
		for _, v := range req.Header[kv.key] {
			if err := enc.WriteField(hpack.HeaderField{Name: kv.lower, Value: v}); err != nil {
				return nil, err
			}
		}
	}
	return b.Bytes(), nil
}

func writeDataFrameSplit(w *bufio.Writer, streamID uint32, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("payload must not be empty")
	}
	// DATA frame header (9 bytes)
	length := len(payload)
	header := [9]byte{}
	header[0] = byte(length >> 16)
	header[1] = byte(length >> 8)
	header[2] = byte(length)
	header[3] = 0x0 // DATA
	header[4] = 0x1 // END_STREAM
	header[5] = byte(streamID >> 24)
	header[6] = byte(streamID >> 16)
	header[7] = byte(streamID >> 8)
	header[8] = byte(streamID)
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if n := len(payload) - 1; n > 0 {
		if _, err := w.Write(payload[:n]); err != nil {
			return err
		}
	}
	return w.Flush()
}

// FireAfter is a convenience helper for deterministic timing windows.
func (e *H2SinglePacketEngine) FireAfter(delay time.Duration) error {
	if delay <= 0 {
		return e.Fire()
	}
	time.Sleep(delay)
	return e.Fire()
}
