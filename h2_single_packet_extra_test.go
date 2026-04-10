package volley

import (
	"net/http"
	"net/url"
	"testing"

	"golang.org/x/net/http2/hpack"
)

func TestEncodeH2HeadersNilURL(t *testing.T) {
	req := &http.Request{Method: http.MethodGet}
	if _, err := encodeH2Headers(req); err == nil {
		t.Fatalf("expected error for nil URL")
	}
}

func TestEncodeH2HeadersHostPriority(t *testing.T) {
	u, _ := url.Parse("https://example.com/path")
	req := &http.Request{Method: http.MethodGet, URL: u, Host: "alt.example.com"}

	block, err := encodeH2Headers(req)
	if err != nil {
		t.Fatalf("encodeH2Headers: %v", err)
	}
	fields, err := hpack.NewDecoder(4096, nil).DecodeFull(block)
	if err != nil {
		t.Fatalf("decode block: %v", err)
	}
	var authority string
	for _, f := range fields {
		if f.Name == ":authority" {
			authority = f.Value
			break
		}
	}
	if authority != "alt.example.com" {
		t.Fatalf("authority mismatch: got=%q", authority)
	}
}

func TestEncodeH2HeadersNilRequest(t *testing.T) {
	if _, err := encodeH2Headers(nil); err == nil {
		t.Fatalf("expected error for nil request")
	}
}

func TestEncodeH2HeadersDefaultSchemeHTTPS(t *testing.T) {
	u, _ := url.Parse("//example.com/path")
	req := &http.Request{Method: http.MethodGet, URL: u, Host: "example.com"}
	block, err := encodeH2Headers(req)
	if err != nil {
		t.Fatalf("encodeH2Headers: %v", err)
	}
	fields, err := hpack.NewDecoder(4096, nil).DecodeFull(block)
	if err != nil {
		t.Fatalf("decode block: %v", err)
	}
	var scheme string
	for _, f := range fields {
		if f.Name == ":scheme" {
			scheme = f.Value
			break
		}
	}
	if scheme != "https" {
		t.Fatalf("scheme mismatch: got=%q", scheme)
	}
}
