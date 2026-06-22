package matcher_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lntt/timetravel/internal/format"
	"github.com/lntt/timetravel/internal/matcher"
)

func TestSequentialMatchAndConsume(t *testing.T) {
	entries := []*format.Entry{
		{Method: "GET", URL: "http://api.example.com/a", Status: 200},
		{Method: "GET", URL: "http://api.example.com/b", Status: 200},
	}
	m := matcher.New(entries, matcher.Sequential, 0, 0)

	r1 := httptest.NewRequest(http.MethodGet, "http://api.example.com/a", nil)
	e, idx, ok := m.MatchWithBody(r1, nil)
	if !ok || idx != 0 || e.Status != 200 {
		t.Fatalf("first match: ok=%v idx=%d", ok, idx)
	}

	r1b := httptest.NewRequest(http.MethodGet, "http://api.example.com/a", nil)
	_, _, ok = m.MatchWithBody(r1b, nil)
	if ok {
		t.Fatal("expected miss after consume")
	}

	r2 := httptest.NewRequest(http.MethodGet, "http://api.example.com/b", nil)
	_, idx, ok = m.MatchWithBody(r2, nil)
	if !ok || idx != 1 {
		t.Fatalf("second match: ok=%v idx=%d", ok, idx)
	}
}

func TestBodyHashDistinguishesPayloads(t *testing.T) {
	entries := []*format.Entry{
		{Method: "POST", URL: "http://x/y", ReqBody: []byte("alpha"), Status: 200},
		{Method: "POST", URL: "http://x/y", ReqBody: []byte("beta"), Status: 201},
	}
	m := matcher.New(entries, matcher.BodyHash, 0, 0)

	r := httptest.NewRequest(http.MethodPost, "http://x/y", nil)
	e, idx, ok := m.MatchWithBody(r, []byte("beta"))
	if !ok || idx != 1 || e.Status != 201 {
		st := 0
		if e != nil {
			st = e.Status
		}
		t.Fatalf("bodyhash match: ok=%v idx=%d status=%d", ok, idx, st)
	}
}

func TestWindowFromTo(t *testing.T) {
	entries := []*format.Entry{
		{Method: "GET", URL: "http://h/0", Status: 200},
		{Method: "GET", URL: "http://h/1", Status: 200},
		{Method: "GET", URL: "http://h/2", Status: 200},
	}
	m := matcher.New(entries, matcher.Sequential, 1, 2)
	r := httptest.NewRequest(http.MethodGet, "http://h/1", nil)
	_, idx, ok := m.MatchWithBody(r, nil)
	if !ok || idx != 1 {
		t.Fatalf("window: ok=%v idx=%d", ok, idx)
	}
	r0 := httptest.NewRequest(http.MethodGet, "http://h/0", nil)
	_, _, ok = m.MatchWithBody(r0, nil)
	if ok {
		t.Fatal("index 0 should be outside window")
	}
}

func TestPathOnlyIncomingMatchesFullRecordedURL(t *testing.T) {
	entries := []*format.Entry{
		{Method: "GET", URL: "http://127.0.0.1:18765/a", Status: 200},
	}
	m := matcher.New(entries, matcher.Sequential, 0, 0)
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18881/a", nil)
	r.Host = "127.0.0.1:18881"
	_, _, ok := m.MatchWithBody(r, nil)
	if !ok {
		t.Fatal("expected path/query match across hosts")
	}
}
