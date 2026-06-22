// Package format implements the .travel binary session format.
//
// File layout:
//
//	[4 bytes: magic "TRVL"]
//	[8 bytes: recording start epoch µs, little-endian]
//	[entries...]
//	  [8 bytes: relative timestamp µs from recording start]
//	  [2 bytes: method length] [N bytes: method]
//	  [4 bytes: URL length] [N bytes: URL]
//	  [4 bytes: request headers length] [N bytes: headers as HTTP/1.1 text]
//	  [4 bytes: request body length] [N bytes: body]
//	  [2 bytes: response status]
//	  [4 bytes: response headers length] [N bytes: response headers]
//	  [4 bytes: response body length] [N bytes: response body]
package format

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const Magic = "TRVL"

// Entry is one recorded request/response exchange.
type Entry struct {
	// RelMicros is microseconds since the recording start (when the response was fully captured).
	RelMicros int64
	Method    string
	URL       string
	// ReqHeaders is HTTP/1.1 header block (Header: value\r\n...), no status line.
	ReqHeaders string
	ReqBody    []byte
	Status     int
	// RespHeaders is HTTP/1.1 header block for the response.
	RespHeaders string
	RespBody    []byte
}

// Index is the zero-based position of this entry in the file.
func (e *Entry) Index() int { return -1 } // set by readers that track index

// WriteHeader writes the file magic and start epoch to w.
func WriteHeader(w io.Writer, start time.Time) error {
	if _, err := io.WriteString(w, Magic); err != nil {
		return err
	}
	us := start.UnixMicro()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(us))
	_, err := w.Write(buf[:])
	return err
}

// ReadHeader reads magic and start epoch from r. Returns the recording start time.
func ReadHeader(r io.Reader) (time.Time, error) {
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return time.Time{}, err
	}
	if string(magic[:]) != Magic {
		return time.Time{}, fmt.Errorf("invalid travel file: bad magic %q", magic)
	}
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return time.Time{}, err
	}
	us := int64(binary.LittleEndian.Uint64(buf[:]))
	return time.UnixMicro(us), nil
}

// WriteEntry appends one entry to w.
func WriteEntry(w io.Writer, e *Entry) error {
	if err := writeU64(w, uint64(e.RelMicros)); err != nil {
		return err
	}
	if err := writeU16String(w, e.Method); err != nil {
		return err
	}
	if err := writeU32Bytes(w, []byte(e.URL)); err != nil {
		return err
	}
	if err := writeU32Bytes(w, []byte(e.ReqHeaders)); err != nil {
		return err
	}
	if err := writeU32Bytes(w, e.ReqBody); err != nil {
		return err
	}
	if err := writeU16(w, uint16(e.Status)); err != nil {
		return err
	}
	if err := writeU32Bytes(w, []byte(e.RespHeaders)); err != nil {
		return err
	}
	return writeU32Bytes(w, e.RespBody)
}

// ReadEntry reads one entry from r. Returns io.EOF at end of file.
func ReadEntry(r io.Reader) (*Entry, error) {
	rel, err := readU64(r)
	if err != nil {
		return nil, err
	}
	method, err := readU16String(r)
	if err != nil {
		return nil, err
	}
	urlb, err := readU32Bytes(r)
	if err != nil {
		return nil, err
	}
	reqH, err := readU32Bytes(r)
	if err != nil {
		return nil, err
	}
	reqB, err := readU32Bytes(r)
	if err != nil {
		return nil, err
	}
	status, err := readU16(r)
	if err != nil {
		return nil, err
	}
	respH, err := readU32Bytes(r)
	if err != nil {
		return nil, err
	}
	respB, err := readU32Bytes(r)
	if err != nil {
		return nil, err
	}
	return &Entry{
		RelMicros:   int64(rel),
		Method:      method,
		URL:         string(urlb),
		ReqHeaders:  string(reqH),
		ReqBody:     reqB,
		Status:      int(status),
		RespHeaders: string(respH),
		RespBody:    respB,
	}, nil
}

// Reader sequentially reads entries from a .travel file.
type Reader struct {
	r      *bufio.Reader
	f      *os.File
	Start  time.Time
	Index  int // next entry index (0-based)
	closer io.Closer
}

// Open opens path for reading.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	start, err := ReadHeader(br)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Reader{r: br, f: f, Start: start, closer: f}, nil
}

// Next reads the next entry. Returns (nil, io.EOF) when done.
func (rd *Reader) Next() (*Entry, error) {
	e, err := ReadEntry(rd.r)
	if err != nil {
		return nil, err
	}
	rd.Index++
	return e, nil
}

// Close closes the underlying file.
func (rd *Reader) Close() error {
	if rd.closer != nil {
		return rd.closer.Close()
	}
	return nil
}

// LoadAll reads every entry from path into memory.
func LoadAll(path string) (start time.Time, entries []*Entry, err error) {
	rd, err := Open(path)
	if err != nil {
		return time.Time{}, nil, err
	}
	defer rd.Close()
	start = rd.Start
	for {
		e, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return start, entries, err
		}
		entries = append(entries, e)
	}
	return start, entries, nil
}

// Writer appends entries to a .travel file (creates/truncates on NewWriter).
type Writer struct {
	w     *bufio.Writer
	f     *os.File
	Start time.Time
}

// NewWriter creates/truncates path and writes the header.
func NewWriter(path string, start time.Time) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriter(f)
	if err := WriteHeader(bw, start); err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{w: bw, f: f, Start: start}, nil
}

// Append writes one entry and flushes.
func (wr *Writer) Append(e *Entry) error {
	if err := WriteEntry(wr.w, e); err != nil {
		return err
	}
	return wr.w.Flush()
}

// Close flushes and closes the file.
func (wr *Writer) Close() error {
	if err := wr.w.Flush(); err != nil {
		wr.f.Close()
		return err
	}
	return wr.f.Close()
}

// HeadersToString converts net/http Header-like map to HTTP/1.1 text.
// keys should be canonicalized; multiple values joined with ", ".
func HeadersToString(h map[string][]string) string {
	if len(h) == 0 {
		return ""
	}
	var b strings.Builder
	for k, vals := range h {
		for _, v := range vals {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

// ParseHeaders parses an HTTP/1.1 header block into a map.
func ParseHeaders(s string) map[string][]string {
	out := make(map[string][]string)
	if s == "" {
		return out
	}
	// Normalize line endings
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
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		out[k] = append(out[k], v)
	}
	return out
}

func writeU16(w io.Writer, v uint16) error {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

func writeU64(w io.Writer, v uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, err := w.Write(buf[:])
	return err
}

func writeU16String(w io.Writer, s string) error {
	if len(s) > 0xffff {
		return fmt.Errorf("string too long for u16 length: %d", len(s))
	}
	if err := writeU16(w, uint16(len(s))); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

func writeU32Bytes(w io.Writer, b []byte) error {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(len(b)))
	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	_, err := w.Write(b)
	return err
}

func readU16(r io.Reader) (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

func readU64(r io.Reader) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

func readU16String(r io.Reader) (string, error) {
	n, err := readU16(r)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readU32Bytes(r io.Reader) ([]byte, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(buf[:])
	if n == 0 {
		return nil, nil
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}
