// Command timetravel is the CLI for Local Network Time-Travel.
//
//	timetravel record  --upstream URL --port 8080 --out session.travel
//	timetravel replay  --file session.travel --port 8080 --speed 0.5
//	timetravel inspect --file session.travel
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lntt/timetravel/internal/format"
	"github.com/lntt/timetravel/internal/matcher"
	"github.com/lntt/timetravel/internal/proxy"
	"github.com/lntt/timetravel/internal/recorder"
	"github.com/lntt/timetravel/internal/replayer"
)

const version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "record":
		os.Exit(cmdRecord(os.Args[2:]))
	case "replay":
		os.Exit(cmdReplay(os.Args[2:]))
	case "inspect":
		os.Exit(cmdInspect(os.Args[2:]))
	case "version", "-version", "--version":
		fmt.Println("timetravel", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `timetravel — Local Network Time-Travel v%s

A local HTTP proxy that records traffic to a .travel file and replays it at
arbitrary speeds (slow-mo races, fast-forward timeouts, exact reproduction).

Usage:
  timetravel record  --upstream URL [--port 8080] [--out session.travel]
  timetravel replay  --file session.travel [--port 8080] [--speed 1.0]
                     [--from N] [--to N] [--match sequential|bodyhash]
                     [--control-port 8081]
  timetravel inspect --file session.travel [--verbose]
  timetravel version

Record mode sits as a reverse proxy in front of --upstream and appends every
exchange to --out. Point your app at http://localhost:<port>.

Replay mode serves recorded responses. Before each response it sleeps
  (t_entry - t_prev) / speed
so speed=0.1 stretches time 10×, speed=10 compresses 10×, speed=0 is instant.

Control API (replay only, default :8081):
  GET  /status
  POST /speed   body: {"speed":0.5}
  POST /pause
  POST /resume
  POST /step

Examples:
  timetravel record --upstream https://api.example.com --port 8080 --out bug.travel
  timetravel replay --file bug.travel --port 8080 --speed 0.1
  timetravel replay --file bug.travel --from 5 --to 20 --speed 0.1
  timetravel inspect --file bug.travel
`, version)
}

func cmdRecord(args []string) int {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	upstream := fs.String("upstream", "", "upstream origin URL (required), e.g. https://api.example.com")
	port := fs.Int("port", 8080, "local listen port")
	out := fs.String("out", "session.travel", "output .travel path")
	_ = fs.Parse(args)

	if *upstream == "" {
		fmt.Fprintln(os.Stderr, "record: --upstream is required")
		fs.Usage()
		return 2
	}

	rec, err := recorder.New(*out)
	if err != nil {
		log.Printf("open recorder: %v", err)
		return 1
	}
	defer rec.Close()

	h, err := proxy.NewRecordHandler(*upstream, rec, log.Default())
	if err != nil {
		log.Printf("proxy: %v", err)
		return 1
	}

	addr := fmt.Sprintf(":%d", *port)
	srv := &http.Server{Addr: addr, Handler: h}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		log.Printf("stopping recorder, flushing %s", *out)
		_ = rec.Close()
		_ = srv.Close()
	}()

	log.Printf("RECORD listening on http://127.0.0.1%s -> %s", addr, *upstream)
	log.Printf("writing %s  (Ctrl+C to stop)", *out)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server: %v", err)
		return 1
	}
	return 0
}

func cmdReplay(args []string) int {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	file := fs.String("file", "", "input .travel path (required)")
	port := fs.Int("port", 8080, "local listen port")
	speed := fs.Float64("speed", 1.0, "speed factor: 1=realtime, 0.1=slow-mo, 10=fast, 0=instant")
	from := fs.Int("from", 0, "first entry index (inclusive)")
	to := fs.Int("to", 0, "last entry index exclusive (0 = end)")
	match := fs.String("match", "sequential", "matching strategy: sequential | bodyhash")
	ctrlPort := fs.Int("control-port", 8081, "control API port (0 to disable)")
	_ = fs.Parse(args)

	if *file == "" {
		fmt.Fprintln(os.Stderr, "replay: --file is required")
		fs.Usage()
		return 2
	}

	start, entries, err := format.LoadAll(*file)
	if err != nil {
		log.Printf("load %s: %v", *file, err)
		return 1
	}
	if len(entries) == 0 {
		log.Printf("%s has no entries", *file)
		return 1
	}

	var strat matcher.Strategy
	switch strings.ToLower(*match) {
	case "sequential", "seq", "":
		strat = matcher.Sequential
	case "bodyhash", "body", "hash":
		strat = matcher.BodyHash
	default:
		log.Printf("unknown --match %q (use sequential or bodyhash)", *match)
		return 2
	}

	srvHandler := replayer.NewServer(entries, start, strat, *from, *to, *speed, log.Default())

	if *ctrlPort > 0 {
		go serveControl(*ctrlPort, srvHandler)
	}

	addr := fmt.Sprintf(":%d", *port)
	httpSrv := &http.Server{Addr: addr, Handler: srvHandler}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		log.Printf("stopping replay")
		_ = httpSrv.Close()
	}()

	window := len(entries)
	if *to > 0 && *to < window {
		window = *to
	}
	window -= *from
	if window < 0 {
		window = 0
	}

	log.Printf("REPLAY listening on http://127.0.0.1%s  speed=%.4g  entries=%d (window %d..%d, %d active)",
		addr, *speed, len(entries), *from, func() int {
			if *to == 0 {
				return len(entries)
			}
			return *to
		}(), window)
	log.Printf("recording started at %s  (%d exchanges)", start.Format(time.RFC3339), len(entries))
	if *ctrlPort > 0 {
		log.Printf("control API on http://127.0.0.1:%d  (GET /status, POST /speed|pause|resume|step)", *ctrlPort)
	}

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server: %v", err)
		return 1
	}
	return 0
}

func serveControl(port int, s *replayer.Server) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		st := replayer.Status{
			Speed:     s.Clock.Speed(),
			Paused:    s.Clock.Paused(),
			Remaining: s.Matcher.Remaining(),
			Total:     len(s.Entries),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("/speed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Speed float64 `json:"speed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.Clock.SetSpeed(body.Speed)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]float64{"speed": s.Clock.Speed()})
	})
	mux.HandleFunc("/pause", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.Clock.Pause()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/resume", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.Clock.Resume()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/step", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.Clock.Step()
		w.WriteHeader(http.StatusNoContent)
	})
	addr := fmt.Sprintf(":%d", port)
	log.Printf("control listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("control server: %v", err)
	}
}

func cmdInspect(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	file := fs.String("file", "", "input .travel path (required)")
	verbose := fs.Bool("verbose", false, "print headers and body sizes")
	_ = fs.Parse(args)

	if *file == "" {
		fmt.Fprintln(os.Stderr, "inspect: --file is required")
		fs.Usage()
		return 2
	}

	start, entries, err := format.LoadAll(*file)
	if err != nil {
		log.Printf("load %s: %v", *file, err)
		return 1
	}

	fmt.Printf("File:      %s\n", *file)
	fmt.Printf("Recorded:  %s\n", start.Format(time.RFC3339Nano))
	fmt.Printf("Entries:   %d\n\n", len(entries))
	fmt.Printf("%-5s %-12s %-6s %-5s %s\n", "IDX", "T+µs", "METHOD", "STAT", "URL")
	fmt.Println(strings.Repeat("-", 100))

	for i, e := range entries {
		fmt.Printf("%-5d %-12d %-6s %-5d %s\n", i, e.RelMicros, e.Method, e.Status, e.URL)
		if *verbose {
			fmt.Printf("      req_headers=%d B  req_body=%d B  resp_headers=%d B  resp_body=%d B\n",
				len(e.ReqHeaders), len(e.ReqBody), len(e.RespHeaders), len(e.RespBody))
			if len(e.RespBody) > 0 && len(e.RespBody) <= 200 && isPrintable(e.RespBody) {
				fmt.Printf("      body: %s\n", string(e.RespBody))
			}
		}
	}

	if len(entries) > 0 {
		last := entries[len(entries)-1]
		fmt.Printf("\nDuration: %s (recorded span)\n", time.Duration(last.RelMicros)*time.Microsecond)
	}
	return 0
}

func isPrintable(b []byte) bool {
	for _, c := range b {
		if c < 9 || (c > 13 && c < 32) {
			return false
		}
	}
	return true
}
