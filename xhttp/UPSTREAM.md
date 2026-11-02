# Upstream provenance

- Repository: https://github.com/justinwoo280/sing-xhttp
- Version: v0.1.6
- Commit: 43219ff199ebe3523a087eea8a84df08c80c9d92
- License: see `library/LICENSE` and `library/NOTICE`

Vendored on 2026-11-02 so this repository is self-contained: the build
workflows previously fetched this module from GitHub at build time
(`go mod download github.com/justinwoo280/sing-xhttp@<latest tag>`).

## Local changes vs. upstream v0.1.6

1. `xhttp/upload_queue.go` — **race fix.** `Push` checked `closed` under
   a mutex, unlocked, then blocked sending on the `pushed` channel,
   while `Close` closed that channel: a Push/Close race panicked with
   "send on closed channel", and a Push blocked on a full queue was
   never released. Push now selects on a separate `done` channel closed
   by `Close` (with a fast-path check so Push after Close fails
   deterministically). Also, `Read` no longer returns `(0, nil)` —
   which violates the `io.Reader` contract — after dropping a stale
   duplicate packet; it keeps reading instead.
2. `xhttp/util.go` — **unbiased `rangeRand`.** The modulo reduction of
   a random uint32 biased results toward small residues and ignored
   `crypto/rand` errors. Now uses `crypto/rand.Int` (rejection
   sampling), falling back to `From` on error.
3. `xhttp/server.go` — **stream-one handler hang.** In
   `handleStreamOne`, the `finished` channel was only closed after the
   `select` waiting on it, and the connection's `onClose` was a no-op,
   so with an asynchronous upper-layer handler `ServeHTTP` hung until
   the request context was cancelled. `onClose` now signals `finished`
   (once), mirroring `handleDownloadGet`. Also removed a redundant
   `Cache-Control` set in `handlePacketUpPost` (already set for every
   request in `ServeHTTP`).
4. `xhttp/client.go` — **nil-TLS guard.** The HTTP/2 branch of
   `buildTransportFactory` dereferenced `tlsConfig` unconditionally;
   it now checks for nil and the H2 dialer fails cleanly instead of
   panicking if a nil config ever reaches it.
5. `xhttp/h1_conn.go` — removed a dead `if isNew` check inside the
   reused-connection stale-response branch (it could never be true).
6. `xhttp/validate.go` — removed a duplicated doc comment line.
7. `xhttp/upload_queue_test.go` — new tests: Push/Close race (run with
   `-race`), in-order reassembly with a stale duplicate, `rangeRand`
   bounds.

Everything else is byte-identical to upstream v0.1.6.
