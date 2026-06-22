# timetravel — Local Network Time-Travel

A **local HTTP proxy** that records every request/response with microsecond timestamps, then **replays** them at any speed. Slow-mo to expose race conditions. Fast-forward to stress timeouts. Exact 1:1 replay to reproduce a bug that only happened once.

Think of it as a **DVR for network traffic**: record the episode once, scrub at any speed.

Zero third-party dependencies. One static-friendly Go binary.

[![CI](https://github.com/Abhineshhh/timetravel/actions/workflows/ci.yml/badge.svg)](https://github.com/Abhineshhh/timetravel/actions/workflows/ci.yml)
[![Release](https://github.com/Abhineshhh/timetravel/actions/workflows/release.yml/badge.svg)](https://github.com/Abhineshhh/timetravel/actions/workflows/release.yml)

## Build

```bash
go build -o timetravel ./cmd/timetravel
# or
go install ./cmd/timetravel
# or use the Makefile (CI-parity)
make test && make build
```

## CI/CD

GitHub Actions pipelines live under [`.github/workflows/`](.github/workflows/).

### CI (`ci.yml`) — every push & PR to `main`

| Job | What it does |
|-----|----------------|
| **Lint** | `gofmt` check, `go vet`, `staticcheck`, `govulncheck` |
| **Test** | Matrix: Ubuntu / Windows / macOS × Go 1.24 & 1.25; race detector (non-Windows); coverage artifact on Linux |
| **Build** | Native binary per OS + smoke (`version` / `help`) |
| **Cross-compile** | linux/darwin/windows × amd64/arm64 (guards the release matrix) |
| **CI Success** | Single gate job for branch protection (`CI Success` required) |

Also runs on **workflow_dispatch** (manual).

### CD (`release.yml`) — version tags

Trigger by pushing a semver tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Or run **Release** manually in Actions (`workflow_dispatch`) with input `tag=v0.1.0`.

Pipeline:

1. **Precheck** — `go vet` + `go test -race`
2. **Build** — six targets (linux/darwin/windows × amd64/arm64), `CGO_ENABLED=0`, version via `-ldflags "-X main.version=…"`
3. **Publish** — GitHub Release with `.tar.gz` / `.zip` archives + `checksums.txt` (SHA-256)

### Dependabot

[`.github/dependabot.yml`](.github/dependabot.yml) opens weekly PRs for Actions + Go modules.

### Branch protection (recommended)

In GitHub → Settings → Branches → protect `main`:

- Require status check: **CI Success**
- Require branches to be up to date before merging

## Quick start

### Record

Sit in front of a real backend as a reverse proxy:

```bash
./timetravel record --upstream https://api.example.com --port 8080 --out session.travel
```

Point your app at `http://127.0.0.1:8080` instead of the real API. Every exchange is appended to `session.travel`. Stop with `Ctrl+C`.

### Replay

Serve the recording (half speed = slow motion):

```bash
./timetravel replay --file session.travel --port 8080 --speed 0.5
```

| `--speed` | Effect |
|-----------|--------|
| `1` | Real-time (original delays) |
| `0.1` | 10× slower — attach a debugger, watch races |
| `10` | 10× faster — compress long sessions / force timeouts |
| `0` | Instant — no sleeps between responses |

Replay a subsequence only:

```bash
./timetravel replay --file session.travel --from 5 --to 20 --speed 0.1
```

Match requests by method+URL+body hash (when the same endpoint is hit with different payloads):

```bash
./timetravel replay --file session.travel --match bodyhash
```

### Inspect

```bash
./timetravel inspect --file session.travel
./timetravel inspect --file session.travel --verbose
```

## Control API (during replay)

By default replay also listens on **`:8081`** for live clock control:

```bash
curl http://127.0.0.1:8081/status
curl -X POST http://127.0.0.1:8081/speed -d '{"speed":0.1}'
curl -X POST http://127.0.0.1:8081/pause
curl -X POST http://127.0.0.1:8081/resume
curl -X POST http://127.0.0.1:8081/step
```

Disable with `--control-port 0`.

## How it works

1. **Proxy (record)** — `internal/proxy` reverse-proxies to `--upstream`, buffers request/response bodies, appends to the `.travel` writer.
2. **Format** — `internal/format` encodes a sequential binary log (`TRVL` magic + epoch µs + entries). No database.
3. **Speed engine** — `internal/replayer.Clock` sleeps `(t_entry − t_prev) / speed` before serving each response. Pause/step interrupt the wait.
4. **Matcher** — `internal/matcher` picks the next unused entry by method+URL (sequential) or method+URL+SHA256(body).
5. **Timestamp rewriter** — `internal/rewriter` shifts `Date`, `Last-Modified`, `Expires`, `Age`, etc. so relative gaps survive while absolute values track “now”.

## `.travel` file format

```
[4 bytes:  "TRVL"]
[8 bytes:  recording start epoch µs, little-endian]
[entries...]
  [8 bytes: relative timestamp µs from recording start]
  [2 bytes: method length] [N bytes: method]
  [4 bytes: URL length] [N bytes: URL]
  [4 bytes: request headers length] [N bytes: HTTP/1.1 header text]
  [4 bytes: request body length] [N bytes: body]
  [2 bytes: response status]
  [4 bytes: response headers length] [N bytes: header text]
  [4 bytes: response body length] [N bytes: body]
```

Append-only while recording. Fully sequential; load into memory for replay/inspect.

## Layout

```
cmd/timetravel/          CLI (record | replay | inspect)
internal/format/         .travel encode/decode
internal/recorder/       append exchanges during record
internal/proxy/          reverse proxy + capture
internal/replayer/       serve + virtual clock
internal/matcher/        request → entry matching
internal/rewriter/       time header shifting
```

## Typical workflows

**Bug reproduction** — Record on staging while reproducing the flake, bring `bug.travel` to your desk, `replay --speed 0.1` with a debugger on the client.

**Retry / timeout tests** — Record a real slow payment call once; `replay --speed 10` in CI so the timeout fires every run.

**Offline backend** — Record once; develop against `replay --speed 0` with no real server.

**Demo a race** — Record the bad sequence; replay in the meeting at `0.1` — no more “works on my machine”.

## Limitations (v0.1)

- HTTP/1.1 only (no HTTP/2 / WebSockets).
- Record mode is **reverse proxy** to one `--upstream` (not a full forward MITM with TLS interception). Terminate TLS elsewhere or use HTTP/internal hosts.
- Concurrent in-flight requests are paced off a shared `lastRel` timeline (good enough for most sessions; not a full per-connection event scheduler).
- Highly nondeterministic request order may need `--match bodyhash` or trimming with `--from`/`--to`.

## License

MIT
