package format_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lntt/timetravel/internal/format"
)

func TestWriteReadRoundTrip(t *testing.T) {
	start := time.Unix(1719060000, 0)
	e := &format.Entry{
		RelMicros:   150_000,
		Method:      "GET",
		URL:         "http://api.example.com/v1/items?q=1",
		ReqHeaders:  "Accept: application/json\r\n",
		ReqBody:     nil,
		Status:      200,
		RespHeaders: "Content-Type: application/json\r\nDate: Mon, 22 Jun 2026 12:00:00 GMT\r\n",
		RespBody:    []byte(`{"ok":true}`),
	}

	var buf bytes.Buffer
	if err := format.WriteHeader(&buf, start); err != nil {
		t.Fatal(err)
	}
	if err := format.WriteEntry(&buf, e); err != nil {
		t.Fatal(err)
	}

	r := bytes.NewReader(buf.Bytes())
	gotStart, err := format.ReadHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	if !gotStart.Equal(start) {
		t.Fatalf("start: got %v want %v", gotStart, start)
	}
	got, err := format.ReadEntry(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.RelMicros != e.RelMicros || got.Method != e.Method || got.URL != e.URL {
		t.Fatalf("meta mismatch: %+v", got)
	}
	if got.Status != e.Status || string(got.RespBody) != string(e.RespBody) {
		t.Fatalf("resp mismatch: %+v", got)
	}
}

func TestWriterReaderFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.travel")
	start := time.Now().UTC().Truncate(time.Microsecond)

	wr, err := format.NewWriter(path, start)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e := &format.Entry{
			RelMicros: int64(i * 100_000),
			Method:    "POST",
			URL:       "http://x/y",
			Status:    201,
			RespBody:  []byte{byte(i)},
		}
		if err := wr.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}

	st, entries, err := format.LoadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries: %d", len(entries))
	}
	_ = st
	_ = os.Remove(path)
}
