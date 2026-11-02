# xhttp

XHTTP transport for sing-box, vendored into this repository so the build
no longer downloads it from another repository at CI time.

## Layout

- `library/` — the XHTTP library itself: a full copy of
  [justinwoo280/sing-xhttp](https://github.com/justinwoo280/sing-xhttp)
  `v0.1.6` (commit `43219ff`), including its tests. Module path stays
  `github.com/justinwoo280/sing-xhttp`; the workflows wire it into
  sing-box with a `replace` directive pointing at this directory.
  Local changes on top of upstream are listed in `UPSTREAM.md`.
- `source/transport/v2ray/xhttp.go` — the sing-box-side bridge file,
  copied verbatim into the sing-box tree by `apply.py`.
- `apply.py` — applies the sing-box-side integration (transport type
  constant, `V2RayXHTTPOptions` option structures, transport dispatch,
  bridge file) to an official SagerNet/sing-box source tree. This is the
  code that previously existed only as a large inline Python heredoc
  inside `.github/workflows/build-xhttp*.yml`.
- `VERSION` — the vendored upstream version (`v0.1.6`).
- `example.json` — minimal VLESS + XHTTP in/out example (the same shape
  the workflows verify with `sing-box check`).

## Usage

Both build workflows do, in the official sing-box tree:

```bash
python3 _self/xhttp/apply.py
go mod edit \
  -require=github.com/justinwoo280/sing-xhttp@v0.1.6 \
  -replace=github.com/justinwoo280/sing-xhttp=./_self/xhttp/library
```

## Updating the vendored library

Replace `library/` with the new upstream tag's tree (excluding `.git`),
update `VERSION` and `UPSTREAM.md`, re-check the local fixes listed in
`UPSTREAM.md` still apply, and run:

```bash
cd library && go vet ./... && go test ./...
```

If upstream changes `xhttp.Options`, update the `V2RayXHTTPOptions`
mirror in `apply.py` to match.
