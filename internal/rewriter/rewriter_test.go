package rewriter_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/lntt/timetravel/internal/rewriter"
)

func TestApplySetsDateAndPreservesContentType(t *testing.T) {
	dst := make(http.Header)
	orig := "Content-Type: application/json\r\nDate: Mon, 22 Jun 2026 12:00:00 GMT\r\n"
	recStart := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	replayStart := time.Now().UTC()
	rewriter.CopyAll(dst, orig)
	rewriter.Apply(dst, orig, recStart, 100*time.Millisecond, replayStart, 1.0)
	if dst.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type: %q", dst.Get("Content-Type"))
	}
	if dst.Get("Date") == "" {
		t.Fatal("expected Date header")
	}
}

func TestCopyAllSkipsHopByHop(t *testing.T) {
	dst := make(http.Header)
	orig := "Connection: keep-alive\r\nX-Custom: yes\r\n"
	rewriter.CopyAll(dst, orig)
	if dst.Get("Connection") != "" {
		t.Fatal("Connection should be skipped")
	}
	if dst.Get("X-Custom") != "yes" {
		t.Fatalf("X-Custom: %q", dst.Get("X-Custom"))
	}
}
