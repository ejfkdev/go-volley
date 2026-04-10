package volley

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/http2/hpack"
)

func TestEncodeH2HeadersIncludesCanonicalHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/a?b=1", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Test", "v1")
	req.Header.Set("User-Agent", "go-volley")

	block, err := encodeH2Headers(req)
	if err != nil {
		t.Fatalf("encodeH2Headers: %v", err)
	}

	dec := hpack.NewDecoder(4096, nil)
	fields, err := dec.DecodeFull(block)
	if err != nil {
		t.Fatalf("decode hpack: %v", err)
	}

	got := make(map[string][]string)
	for _, f := range fields {
		got[f.Name] = append(got[f.Name], f.Value)
	}

	if got[":method"][0] != http.MethodPost {
		t.Fatalf("method mismatch: %v", got[":method"])
	}
	if got[":path"][0] != "/a?b=1" {
		t.Fatalf("path mismatch: %v", got[":path"])
	}
	if got["x-test"][0] != "v1" {
		t.Fatalf("x-test missing: %#v", got)
	}
	if got["user-agent"][0] != "go-volley" {
		t.Fatalf("user-agent missing: %#v", got)
	}
}
