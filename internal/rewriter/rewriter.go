// Package rewriter adjusts time-related HTTP response headers during replay
// so relative age/expiry relationships are preserved while absolute values
// track the current wall clock.
package rewriter

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// timeHeaders are shifted by the same absolute offset (replayNow - originalDate).
var timeHeaders = []string{
	"Date",
	"Last-Modified",
	"Expires",
	"If-Modified-Since",
	"If-Unmodified-Since",
}

// Apply shifts time headers on dst based on the original response headers and
// how much wall time has advanced since the original recording's Date (or
// recordingStart if Date is missing).
//
// origHeaders is the recorded response header block (HTTP/1.1 text or empty).
// recordingStart is when the session was recorded (file header epoch).
// entryRel is the entry's relative timestamp from recording start.
// replayStart is when this replay session began.
// speed is the current speed factor (used only for Age scaling conceptually;
// Age is set from actual elapsed replay time when possible).
func Apply(dst http.Header, origHeaders string, recordingStart time.Time, entryRel time.Duration, replayStart time.Time, speed float64) {
	orig := parseHeaderBlock(origHeaders)

	// Original absolute time for this response (prefer Date header).
	origAbs := recordingStart.Add(entryRel)
	if d, ok := parseHTTPDate(first(orig, "Date")); ok {
		origAbs = d
	}

	// Replay absolute time: if we were at real-time, this response would appear at
	// replayStart + entryRel/speed. We use current time as the served Date to match
	// "live" expectations, and shift other absolute headers by the same delta.
	now := time.Now().UTC()
	delta := now.Sub(origAbs)

	for _, name := range timeHeaders {
		vals, ok := orig[http.CanonicalHeaderKey(name)]
		if !ok {
			// try exact key from parse
			vals, ok = orig[name]
		}
		if !ok || len(vals) == 0 {
			continue
		}
		if t, ok := parseHTTPDate(vals[0]); ok {
			dst.Set(name, t.Add(delta).UTC().Format(http.TimeFormat))
		}
	}

	// Always set Date to now for freshness unless caller wants strict mode.
	if dst.Get("Date") == "" {
		dst.Set("Date", now.Format(http.TimeFormat))
	}

	// Age: increment by how long since replay started, scaled conceptually.
	if ageStr := first(orig, "Age"); ageStr != "" {
		if ageSec, err := strconv.ParseInt(strings.TrimSpace(ageStr), 10, 64); err == nil {
			elapsed := time.Since(replayStart)
			if speed > 0 {
				// Age advances with real time during replay (client-observed latency).
				ageSec += int64(elapsed.Seconds())
			}
			dst.Set("Age", strconv.FormatInt(ageSec, 10))
		}
	}

	// Copy non-time headers that were not already set by the replayer.
	skip := map[string]bool{
		"Date": true, "Last-Modified": true, "Expires": true,
		"If-Modified-Since": true, "If-Unmodified-Since": true, "Age": true,
		// hop-by-hop / length handled elsewhere
		"Transfer-Encoding": true, "Connection": true, "Keep-Alive": true,
	}
	for k, vals := range orig {
		ck := http.CanonicalHeaderKey(k)
		if skip[ck] {
			continue
		}
		if dst.Get(ck) != "" {
			continue
		}
		for _, v := range vals {
			dst.Add(ck, v)
		}
	}
}

// CopyAll copies all headers from the recorded block into dst (no time shift).
// Used when building the base response before Apply.
func CopyAll(dst http.Header, origHeaders string) {
	orig := parseHeaderBlock(origHeaders)
	for k, vals := range orig {
		ck := http.CanonicalHeaderKey(k)
		if ck == "Transfer-Encoding" || ck == "Connection" || ck == "Keep-Alive" {
			continue
		}
		for _, v := range vals {
			dst.Add(ck, v)
		}
	}
}

func parseHeaderBlock(s string) map[string][]string {
	out := make(map[string][]string)
	if s == "" {
		return out
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		k := http.CanonicalHeaderKey(strings.TrimSpace(line[:i]))
		v := strings.TrimSpace(line[i+1:])
		out[k] = append(out[k], v)
	}
	return out
}

func first(h map[string][]string, key string) string {
	ck := http.CanonicalHeaderKey(key)
	if vals, ok := h[ck]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

func parseHTTPDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	formats := []string{
		http.TimeFormat,
		time.RFC1123,
		time.RFC1123Z,
		time.RFC850,
		time.ANSIC,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
