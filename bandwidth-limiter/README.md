# bandwidth-limiter

`bandwidth-limiter` outbound ported from
[shtorm-7/sing-box-extended](https://github.com/shtorm-7/sing-box-extended)
(`protocol/limiter/bandwidth`, commit 5956780) to official SagerNet/sing-box.

`build-xhttp.yml` runs `apply.py` inside the official sing-box tree before building.

Changes vs. upstream:
- `route.NewRouter` call adapted to the official signature (no name argument)
- removed `hwid` / `mux` connection types and flow keys (not available in official sing-box)
- removed `manager` strategy (needs extended's manager service)
- fix: `users` strategy now uses each user's own `speed` (upstream used the top-level speed)
- missing `speed` returns a config error instead of panicking
- fix: `route.final` is honoured. With no rules of its own the outbound now
  hands the connection straight to its final outbound. With rules, the
  sub-router's fallback (`Default()`) resolves to `route.final` instead of the
  global default outbound, which before could silently send traffic elsewhere
  or straight back into the limiter.
- fix: the declared dependencies now include `route.final` and every outbound
  named by a route rule (nested logical rules included), so those outbounds are
  started before this one.
- fix: a single read/write larger than the 64 KiB burst is consumed in pieces
  instead of failing with `exceeds limiter's burst` and dropping the connection.
- fix: `FairQueueLimiter` propagates the underlying limiter's error instead of
  reporting success after a cancelled or rejected wait.
- `protocol/limiter/bandwidth/limiter_test.go`: unit tests for the three points
  above plus a `route.final` routing test; `build-xhttp-limiter.yml` runs
  `go test ./protocol/limiter/bandwidth` on every build.

## Options

- `strategy`: `global` | `connection` | `users` | `bypass`
- `mode`: `upload` | `download` | `bidirectional`
- `connection_type` (connection strategy): `default` | `source_ip`
- `flow_keys` (fair queue inside one limit): `user` | `source_ip` | `protocol` | `destination`
- `speed`: e.g. `100KB`, `5MB`
- `route`: nested route (`rules`, `final`) used after limiting

Per inbound: route rule `{"inbound": ["tag"], "outbound": "<global limiter>"}`.
Per user: route rule `{"auth_user": ["name"], "outbound": "<users limiter>"}`.
See `example.json`.
