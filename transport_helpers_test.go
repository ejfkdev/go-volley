package volley

import (
	"bufio"
	"bytes"
	"net/http"
	"testing"
)

func TestWriteDataFrameSplit_HoldsLastByte(t *testing.T) {
	var out bytes.Buffer
	w := bufio.NewWriter(&out)

	if err := writeDataFrameSplit(w, 1, []byte("abc")); err != nil {
		t.Fatalf("writeDataFrameSplit: %v", err)
	}

	b := out.Bytes()
	if len(b) != 11 { // 9-byte header + 2 bytes payload prefix
		t.Fatalf("unexpected frame length: got=%d want=11", len(b))
	}
	if b[0] != 0 || b[1] != 0 || b[2] != 3 { // declared data length remains full payload size
		t.Fatalf("unexpected declared length: %v %v %v", b[0], b[1], b[2])
	}
	if b[3] != 0x0 || b[4] != 0x1 { // DATA + END_STREAM
		t.Fatalf("unexpected type/flags: %x %x", b[3], b[4])
	}
	if !bytes.Equal(b[9:], []byte("ab")) {
		t.Fatalf("unexpected payload prefix: %q", b[9:])
	}
}

func TestWriteDataFrameSplit_SingleBytePayload(t *testing.T) {
	var out bytes.Buffer
	w := bufio.NewWriter(&out)

	if err := writeDataFrameSplit(w, 3, []byte("x")); err != nil {
		t.Fatalf("writeDataFrameSplit: %v", err)
	}

	b := out.Bytes()
	if len(b) != 9 { // only frame header is sent before held-byte fire
		t.Fatalf("unexpected frame length: got=%d want=9", len(b))
	}
	if b[0] != 0 || b[1] != 0 || b[2] != 1 {
		t.Fatalf("unexpected declared length: %v %v %v", b[0], b[1], b[2])
	}
}

func TestNextProtos(t *testing.T) {
	if got := nextProtos(false); len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("unexpected nextProtos(false): %#v", got)
	}
	if got := nextProtos(true); len(got) != 2 || got[0] != "h2" || got[1] != "http/1.1" {
		t.Fatalf("unexpected nextProtos(true): %#v", got)
	}
}

func TestEnginePendingQueue(t *testing.T) {
	e := NewEngine()
	req := &http.Request{} // intentionally invalid; Do() returns quickly with error

	if got := e.Pending(); got != 0 {
		t.Fatalf("initial pending mismatch: %d", got)
	}
	e.Queue(req, nil)
	e.Queue(req, nil)
	if got := e.Pending(); got != 2 {
		t.Fatalf("pending mismatch: got=%d want=2", got)
	}
}
