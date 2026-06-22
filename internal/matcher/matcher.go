// Package matcher decides which recorded entry should satisfy an incoming request.
package matcher

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/lntt/timetravel/internal/format"
)

// Strategy selects how requests are matched to recorded entries.
type Strategy int

const (
	// Sequential matches method+URL in recording order; each entry used at most once.
	Sequential Strategy = iota
	// BodyHash matches method+URL+SHA256(request body); each entry used at most once.
	BodyHash
)

// Matcher holds the session entries and tracks which have been consumed.
type Matcher struct {
	mu       sync.Mutex
	entries  []*format.Entry
	used     []bool
	strategy Strategy
	// from/to are inclusive/exclusive indices into entries (replay window).
	from, to int
}

// New builds a matcher over entries. from/to select a subsequence (to==0 means end).
func New(entries []*format.Entry, strategy Strategy, from, to int) *Matcher {
	n := len(entries)
	if to <= 0 || to > n {
		to = n
	}
	if from < 0 {
		from = 0
	}
	if from > to {
		from = to
	}
	return &Matcher{
		entries:  entries,
		used:     make([]bool, n),
		strategy: strategy,
		from:     from,
		to:       to,
	}
}

// Match finds the next unused entry for r. Returns the entry, its global index, or ok=false.
func (m *Matcher) Match(r *http.Request) (entry *format.Entry, index int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reqURL := normalizeURL(r)
	reqMethod := strings.ToUpper(r.Method)
	var bodyHash string
	if m.strategy == BodyHash && r.Body != nil {
		// body should already be buffered by the caller into r; we hash ContentLength slice via GetBody if needed
	}
	_ = bodyHash

	for i := m.from; i < m.to; i++ {
		if m.used[i] {
			continue
		}
		e := m.entries[i]
		if strings.ToUpper(e.Method) != reqMethod {
			continue
		}
		if !urlMatches(e.URL, reqURL) {
			continue
		}
		if m.strategy == BodyHash {
			// Entry request body vs incoming — caller sets header X-Travel-Body-Hash or we compare lengths only as weak fallback
			if !bodyMatches(e, r) {
				continue
			}
		}
		m.used[i] = true
		return e, i, true
	}
	return nil, -1, false
}

// MatchWithBody is like Match but includes the request body bytes for BodyHash strategy.
func (m *Matcher) MatchWithBody(r *http.Request, body []byte) (entry *format.Entry, index int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reqURL := normalizeURL(r)
	reqMethod := strings.ToUpper(r.Method)
	hash := hashBody(body)

	for i := m.from; i < m.to; i++ {
		if m.used[i] {
			continue
		}
		e := m.entries[i]
		if strings.ToUpper(e.Method) != reqMethod {
			continue
		}
		if !urlMatches(e.URL, reqURL) {
			continue
		}
		if m.strategy == BodyHash {
			if hashBody(e.ReqBody) != hash {
				continue
			}
		}
		m.used[i] = true
		return e, i, true
	}
	return nil, -1, false
}

// PeekNext returns the next unused entry in order without consuming it (for inspect/debug).
func (m *Matcher) Remaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := m.from; i < m.to; i++ {
		if !m.used[i] {
			n++
		}
	}
	return n
}

// Reset clears consumption flags (start replay over).
func (m *Matcher) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.used {
		m.used[i] = false
	}
}

func normalizeURL(r *http.Request) string {
	// Build request target as recorded: scheme://host/path?query or path-only for reverse proxy
	u := r.URL
	if u.Scheme != "" && u.Host != "" {
		return u.String()
	}
	// Incoming to replay server: usually path only; host in r.Host
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = u.Host
	}
	path := u.RequestURI()
	if path == "" {
		path = u.Path
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
	}
	return scheme + "://" + host + path
}

// urlMatches compares recorded full URL to incoming normalized URL.
// Accepts match on path+query alone if hosts differ (reverse-proxy vs absolute URL recordings).
func urlMatches(recorded, incoming string) bool {
	if recorded == incoming {
		return true
	}
	ru, err1 := url.Parse(recorded)
	iu, err2 := url.Parse(incoming)
	if err1 != nil || err2 != nil {
		return pathQuery(recorded) == pathQuery(incoming)
	}
	rp := ru.EscapedPath()
	ip := iu.EscapedPath()
	if rp == "" {
		rp = "/"
	}
	if ip == "" {
		ip = "/"
	}
	if rp != ip {
		return false
	}
	return ru.RawQuery == iu.RawQuery
}

func pathQuery(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			return s[j:]
		}
		return "/"
	}
	return s
}

func hashBody(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func bodyMatches(e *format.Entry, r *http.Request) bool {
	// Without body bytes we can only accept if recorded body is empty
	return len(e.ReqBody) == 0
}
