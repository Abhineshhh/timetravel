// Package recorder writes live proxy traffic into a .travel file.
package recorder

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/lntt/timetravel/internal/format"
)

// Recorder appends exchanges to a travel writer. Safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	wr    *format.Writer
	start time.Time
}

// New creates a recorder writing to path.
func New(path string) (*Recorder, error) {
	start := time.Now().UTC()
	wr, err := format.NewWriter(path, start)
	if err != nil {
		return nil, err
	}
	return &Recorder{wr: wr, start: start}, nil
}

// Start returns the recording start time.
func (r *Recorder) Start() time.Time { return r.start }

// Record saves one completed exchange.
func (r *Recorder) Record(method, urlStr string, reqHeader http.Header, reqBody []byte, status int, respHeader http.Header, respBody []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel := time.Since(r.start).Microseconds()
	e := &format.Entry{
		RelMicros:   rel,
		Method:      method,
		URL:         urlStr,
		ReqHeaders:  format.HeadersToString(headerMap(reqHeader)),
		ReqBody:     append([]byte(nil), reqBody...),
		Status:      status,
		RespHeaders: format.HeadersToString(headerMap(respHeader)),
		RespBody:    append([]byte(nil), respBody...),
	}
	return r.wr.Append(e)
}

// Close flushes the travel file.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wr.Close()
}

// Drain copies rc fully into memory and returns bytes plus a new ReadCloser over them.
func Drain(rc io.ReadCloser) ([]byte, io.ReadCloser, error) {
	if rc == nil {
		return nil, http.NoBody, nil
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, err
	}
	return b, io.NopCloser(bytes.NewReader(b)), nil
}

func headerMap(h http.Header) map[string][]string {
	if h == nil {
		return nil
	}
	m := make(map[string][]string, len(h))
	for k, v := range h {
		cp := make([]string, len(v))
		copy(cp, v)
		m[k] = cp
	}
	return m
}
