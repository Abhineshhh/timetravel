// Package replayer serves recorded .travel sessions with a controllable virtual clock.
package replayer

import (
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/lntt/timetravel/internal/format"
	"github.com/lntt/timetravel/internal/matcher"
	"github.com/lntt/timetravel/internal/rewriter"
)

// Clock controls how fast recorded delays elapse during replay.
// Speed 1.0 = real-time, 0.1 = 10× slower, 10 = 10× faster, 0 = no delay.
type Clock struct {
	mu       sync.Mutex
	speed    float64
	paused   bool
	stepCh   chan struct{} // non-nil while paused; send to step one exchange
	replayT0 time.Time     // wall time when replay started
	lastRel  time.Duration // last served entry relative time (recorded)
	cond     *sync.Cond
}

// NewClock creates a clock at the given speed factor.
func NewClock(speed float64) *Clock {
	if speed < 0 {
		speed = 0
	}
	c := &Clock{speed: speed, replayT0: time.Now(), stepCh: make(chan struct{}, 1)}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Speed returns the current factor.
func (c *Clock) Speed() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.speed
}

// SetSpeed updates the speed factor (0 = instant).
func (c *Clock) SetSpeed(s float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s < 0 {
		s = 0
	}
	c.speed = s
	c.cond.Broadcast()
}

// Pause freezes the timeline until Resume or Step.
func (c *Clock) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused = true
}

// Resume unfreezes the timeline.
func (c *Clock) Resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused = false
	c.cond.Broadcast()
}

// Step advances one waiting exchange while paused.
func (c *Clock) Step() {
	c.mu.Lock()
	select {
	case c.stepCh <- struct{}{}:
	default:
	}
	c.cond.Broadcast()
	c.mu.Unlock()
}

// Paused reports pause state.
func (c *Clock) Paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

// WaitUntilEntry delays until the recorded relative time for this entry,
// adjusted by speed. Concurrent requests each wait independently based on
// their own entry timestamp relative to the previous served timestamp in
// this goroutine's flow — we use global lastRel as baseline for sequential
// pacing, which approximates original inter-response gaps.
//
// For entry at recorded relative time T, we sleep max(0, (T - lastRel) / speed)
// then set lastRel = T. First entry uses T / speed from replay start.
func (c *Clock) WaitUntilEntry(entryRel time.Duration) {
	c.mu.Lock()
	for c.paused {
		// wait for resume or step
		select {
		case <-c.stepCh:
			// allow this one exchange through without full delay when stepped
			c.mu.Unlock()
			return
		default:
		}
		c.cond.Wait()
		// re-check step after wake
		select {
		case <-c.stepCh:
			c.mu.Unlock()
			return
		default:
		}
	}
	speed := c.speed
	prev := c.lastRel
	c.mu.Unlock()

	delta := entryRel - prev
	if delta < 0 {
		delta = 0
	}

	var sleepFor time.Duration
	if speed == 0 {
		sleepFor = 0
	} else {
		sleepFor = time.Duration(float64(delta) / speed)
	}

	if sleepFor > 0 {
		// Interruptible sleep respecting pause mid-wait
		deadline := time.Now().Add(sleepFor)
		for {
			c.mu.Lock()
			if c.paused {
				for c.paused {
					select {
					case <-c.stepCh:
						c.lastRel = entryRel
						c.mu.Unlock()
						return
					default:
					}
					c.cond.Wait()
					select {
					case <-c.stepCh:
						c.lastRel = entryRel
						c.mu.Unlock()
						return
					default:
					}
				}
				// resumed: recompute remaining
				remaining := time.Until(deadline)
				speed = c.speed
				c.mu.Unlock()
				if remaining <= 0 || speed == 0 {
					break
				}
				deadline = time.Now().Add(remaining)
				continue
			}
			c.mu.Unlock()

			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			chunk := remaining
			if chunk > 50*time.Millisecond {
				chunk = 50 * time.Millisecond
			}
			time.Sleep(chunk)
		}
	}

	c.mu.Lock()
	if entryRel > c.lastRel {
		c.lastRel = entryRel
	}
	c.mu.Unlock()
}

// Reset clears pacing state (e.g. after matcher reset).
func (c *Clock) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastRel = 0
	c.replayT0 = time.Now()
}

// ReplayStart returns when this clock/replay began.
func (c *Clock) ReplayStart() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.replayT0
}

// Server is the HTTP handler that serves replayed responses.
type Server struct {
	Entries        []*format.Entry
	RecordingStart time.Time
	Matcher        *matcher.Matcher
	Clock          *Clock
	Log            *log.Logger
	Strategy       matcher.Strategy
}

// NewServer loads configuration for replay.
func NewServer(entries []*format.Entry, recordingStart time.Time, strat matcher.Strategy, from, to int, speed float64, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{
		Entries:        entries,
		RecordingStart: recordingStart,
		Matcher:        matcher.New(entries, strat, from, to),
		Clock:          NewClock(speed),
		Log:            logger,
		Strategy:       strat,
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if r.Body != nil {
		r.Body.Close()
	}
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	var (
		entry *format.Entry
		idx   int
		ok    bool
	)
	if s.Strategy == matcher.BodyHash {
		entry, idx, ok = s.Matcher.MatchWithBody(r, body)
	} else {
		entry, idx, ok = s.Matcher.MatchWithBody(r, body) // sequential still works; body ignored
	}
	if !ok {
		s.Log.Printf("MISS %s %s (remaining=%d)", r.Method, r.URL.RequestURI(), s.Matcher.Remaining())
		http.Error(w, "timetravel: no matching recorded response", http.StatusNotFound)
		return
	}

	entryRel := time.Duration(entry.RelMicros) * time.Microsecond
	s.Clock.WaitUntilEntry(entryRel)

	// Build response
	for k := range w.Header() {
		delete(w.Header(), k)
	}
	rewriter.CopyAll(w.Header(), entry.RespHeaders)
	rewriter.Apply(w.Header(), entry.RespHeaders, s.RecordingStart, entryRel, s.Clock.ReplayStart(), s.Clock.Speed())
	w.Header().Set("Content-Length", strconv.Itoa(len(entry.RespBody)))
	w.Header().Set("X-TimeTravel-Index", strconv.Itoa(idx))
	w.Header().Set("X-TimeTravel-Rel-Us", strconv.FormatInt(entry.RelMicros, 10))

	w.WriteHeader(entry.Status)
	if _, err := w.Write(entry.RespBody); err != nil {
		s.Log.Printf("write error: %v", err)
		return
	}
	s.Log.Printf("REPLAY #%d %s %s -> %d (%d B) [t=%s]",
		idx, entry.Method, r.URL.RequestURI(), entry.Status, len(entry.RespBody), entryRel)
}

// Status is a snapshot of replay state for the control API / CLI.
type Status struct {
	Speed     float64 `json:"speed"`
	Paused    bool    `json:"paused"`
	Remaining int     `json:"remaining"`
	Total     int     `json:"total"`
}
