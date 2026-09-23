# sing-box Router Connection Limiter

Source repository:
https://github.com/zakuwaki/sing-box

Source branch:
self-use

Source commit:
8a63fb5

Feature:
Router-layer connection bandwidth and timeout limiter.

This directory is a source backup of the limiter-related changes.
It is NOT a standalone Go module.

The files are preserved under their original sing-box source paths
so they can later be merged into an official SagerNet/sing-box source tree.

## Limiter source files

adapter/router.go
adapter/rule.go
box.go
limiter/builder.go
limiter/limiter.go
option/limiter.go
option/options.go
option/rule.go
route/router.go
route/route.go
route/rule/rule_abstract.go

## Dependency

limiter/builder.go uses:
github.com/dustin/go-humanize

limiter/limiter.go uses:
golang.org/x/time/rate
