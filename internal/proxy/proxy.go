// Package proxy provides the reverse-proxy handler used in record mode.
package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/lntt/timetravel/internal/recorder"
)

// RecordHandler forwards requests to an upstream origin and records each exchange.
type RecordHandler struct {
	Upstream *url.URL
	Rec      *recorder.Recorder
	Log      *log.Logger
}

// NewRecordHandler builds a recording reverse-proxy handler.
func NewRecordHandler(upstreamURL string, rec *recorder.Recorder, logger *log.Logger) (*RecordHandler, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.Default()
	}
	return &RecordHandler{Upstream: u, Rec: rec, Log: logger}, nil
}

// ServeHTTP implements http.Handler.
func (h *RecordHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var reqBody []byte
	if r.Body != nil {
		b, rc, err := recorder.Drain(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		reqBody = b
		r.Body = rc
		r.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(reqBody)), nil
		}
		r.ContentLength = int64(len(reqBody))
	}

	outURL := *r.URL
	outURL.Scheme = h.Upstream.Scheme
	outURL.Host = h.Upstream.Host
	if h.Upstream.Path != "" && h.Upstream.Path != "/" {
		outURL.Path = singleJoiningSlash(h.Upstream.Path, r.URL.Path)
	}
	urlStr := outURL.String()

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = h.Upstream.Scheme
			req.URL.Host = h.Upstream.Host
			if h.Upstream.Path != "" && h.Upstream.Path != "/" {
				req.URL.Path = singleJoiningSlash(h.Upstream.Path, req.URL.Path)
			}
			req.Host = h.Upstream.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			respBody, rc, err := recorder.Drain(resp.Body)
			if err != nil {
				return err
			}
			resp.Body = rc
			resp.ContentLength = int64(len(respBody))
			resp.Header.Set("Content-Length", strconv.Itoa(len(respBody)))

			if err := h.Rec.Record(r.Method, urlStr, r.Header.Clone(), reqBody, resp.StatusCode, resp.Header.Clone(), respBody); err != nil {
				h.Log.Printf("record error: %v", err)
			} else {
				h.Log.Printf("recorded %s %s -> %d (%d B)", r.Method, urlStr, resp.StatusCode, len(respBody))
			}
			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			h.Log.Printf("proxy error: %v", err)
			http.Error(rw, "bad gateway: "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
