# diameter-ping

`dping` checks a Diameter peer the way `ping` checks a host: CER/CEA, a few DWR/DWA with round-trip times, then DPR/DPA, over TCP or SCTP. When the peer refuses, it prints the Result-Code by its IANA name and what usually causes it. Go standard library only, version 0.1.0, MIT, public repo.

## Layout

1. `cmd/dping`: flags and main loop (`main.go`), text and JSON output (`report.go`), failure hints per stage (`hints.go`).
2. `internal/diameter`: RFC 6733 codec, plus IANA names embedded from the generated `iana.json`.
3. `internal/peer`: dial, capabilities exchange, watchdog, disconnect. SCTP is raw `syscall` in `sctp_linux.go`; `sctp_other.go` returns `ErrSCTPUnsupported`.
4. `internal/fakepeer`: scripted peer for the unit tests, TCP and SCTP on 127.0.0.1.
5. `tools/gendata`: regenerates `internal/diameter/iana.json` from the IANA AAA Parameters registry.
6. `interop/`: separate Go module that runs the real binary against go-diameter. `interop/demo` records `docs/demo.gif`.
7. `.github/workflows/ci.yml`: jobs `test` (Go 1.22 and stable), `lint` (staticcheck) and `interop`.

## Commands

Verified 2026-10-17, from the repo root:

1. `go build ./...` and `GOOS=darwin go build ./... && GOOS=windows go build ./...`: clean.
2. `test -z "$(gofmt -l .)" && go vet ./...`: clean.
3. `go test -race ./...`: passes, SCTP cases included.
4. `go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...`: clean.
5. `(cd interop && go test -race ./...)`: the real binary against go-diameter on 127.0.0.1, TCP and SCTP, passes.
6. Smoke run, no peer needed: `go build ./cmd/dping && ./dping -c 1 -origin-host dping.test 127.0.0.1:3869; rm dping` prints connection refused plus a hint and exits 2. Add `-sctp` for the SCTP path. `go run ./cmd/dping -version` prints the version.

Not run:

1. `go run ./tools/gendata`: fetches the IANA registry and rewrites `internal/diameter/iana.json`.
2. `(cd interop && go run ./demo)`: rewrites `docs/demo.gif`, needs `agg` on PATH and 127.0.0.1:3868 free.
3. `dping -app s6a -origin-host <host> <peer>` against a real Diameter peer.
4. `git push`, tags, releases.

## Conventions

1. One-line conventional commits without scope: `feat: x`, `fix: x`, `docs: x`. No AI attribution, no Co-Authored-By.
2. Solo repo, commit straight to `main`. After a push, follow CI with `gh run list --commit <sha>` until all three jobs are green.
3. No tags or GitHub releases yet. Users run `go install github.com/alb777/diameter-ping/cmd/dping@latest`, which builds `main`. The version is the `version` constant in `cmd/dping/main.go`. Release binaries are only an idea for v0.2.
4. The main module stays stdlib only. A dependency goes in `interop/`, which has its own `go.mod`.
5. Code comments cite the RFC 6733 section they implement. Keep doing that.
6. Tests and docs use `.test` and `.example` names and 127.0.0.1. Never commit real peer hosts, addresses or captures.
7. When user-visible output changes, update the README examples and re-record `docs/demo.gif`. The GIF only ever comes from real runs.

## Gotchas

1. The main module targets Go 1.22 and CI builds it with 1.22, so no newer stdlib APIs there. `interop/` needs Go 1.26 or later.
2. SCTP tests skip instead of failing when the kernel has no SCTP, so a green run may not have tested it. Check with `go test -v ./internal/peer | grep sctp`. The in-tree `sctp` module usually loads on first socket use; CI runs `sudo modprobe sctp`.
3. SCTP is Linux only. Keep `sctp_linux.go` and `sctp_other.go` in step; the CI cross-build for macOS and Windows catches drift.
4. go-diameter's state machine has no DPR handler, so `interop/peer.go` adds one. Without it dping's DPR goes unanswered.
5. Text output wraps at `width` (100) in `report.go` without splitting words, and a unit test checks line length. An early version cut hints mid-word.
6. Never hand-edit `iana.json`. It is generated and embedded with `go:embed`.
7. The default `-app relay` gets 5010 from peers that only accept known clients. Expected, not a bug.
8. `interop/demo` refuses to record when a run's output lacks the expected strings, so a failure there usually means the output changed.
9. `go build ./cmd/dping` leaves a `dping` binary in the root. It is gitignored; delete it anyway.
