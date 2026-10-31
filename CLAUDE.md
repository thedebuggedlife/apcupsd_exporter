# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Prometheus exporter for the [apcupsd](http://www.apcupsd.org/) Network Information Server (NIS), maintained as a fork of `mdlayher/apcupsd_exporter`.

**Modernization in progress.** GitHub issue #1 is the spec for `v0.4.0`, the fork's first tagged release. Read it before starting any step: `gh issue view 1 --repo thedebuggedlife/apcupsd_exporter`. Its steps must be done in order, and its findings are referenced below as F1, F2, and so on. This file describes the code as it stands today, so update it in the same change that makes any of it stale. Step 6 of the issue moves this content into `AGENTS.md`, with `CLAUDE.md` importing it.

## Commands

```sh
go build ./...                                  # compile check, writes no binary
go test -race ./...                             # what CI runs
go test -run 'TestUPSCollector/full' -v .       # single table case; root package holds all tests today
go vet ./...
staticcheck ./...                               # CI installs honnef.co/go/tools/cmd/staticcheck@latest
gofmt -l .                                      # not in CI yet; flags cmd/apcupsd_exporter/main.go (F9)
go run ./cmd/apcupsd_exporter -apcupsd.addr=127.0.0.1:3551
docker build -t apcupsd_exporter .
```

There is no Makefile and no `.gitignore`. `go build ./cmd/apcupsd_exporter` drops a binary in the repo root, so use `-o` to put it elsewhere.

You can smoke-test without a UPS. With an unreachable `-apcupsd.addr`, the exporter starts normally and `/metrics` returns 500.

## Architecture

Three layers, each constructing the next:

1. `cmd/apcupsd_exporter/main.go` parses flags, registers an `Exporter` on the default Prometheus registry, and serves HTTP. It supplies a `ClientFunc` that dials the NIS.
2. `apcupsdexporter.go`: `Exporter` is stateless. Every `Collect` creates a 5s context, dials a new connection, builds a new `UPSCollector`, runs it, and closes the connection. Nothing is cached between scrapes.
3. `upscollector.go`: `UPSCollector` turns one `apcupsd.Status` into const metrics. It reads through the `StatusSource` interface, which is also the test seam (`testStatusSource`).

Behaviour that spans these layers:

- **Failure is all-or-nothing.** A dial or `Status()` error becomes `prometheus.NewInvalidMetric`, which fails the gather, so promhttp returns 500 for the whole page, including `go_*` and `process_*`. Prometheus records this as `up == 0`. `NewUPSCollector(nil).Info` in `Exporter.Collect` exists only to get a `Desc` for that error, and a nil-source collector must never have `Collect` called.
- **One bad field fails the scrape.** The library's `Status()` returns an error if any single NIS field fails to parse.
- **Unreported fields read as 0.** `apcupsd.Status` is a plain struct, so a field the UPS doesn't report can't be told apart from a real zero (F5).
- **`Exporter.Describe` touches the network.** It dials the NIS, so registration blocks on it and the collector is only checked if the daemon was up at startup (F2).
- **The 5s timeout covers the dial only.** `Client.Status()` has no deadline (F1).
- **`/` is a catch-all 301 to the metrics path.** Any route not explicitly registered redirects there, so a redirect-following client ends up on `/metrics`, which dials apcupsd. `-telemetry.path=/` panics at startup with a duplicate mux registration, and any new route has the same conflict if `-telemetry.path` is set to it.
- **`-metrics.system=false` is only partial.** `promhttp_metric_handler_*` stays in the output (F3).

## Testing notes

- All tests run the collector through `testCollector`, which uses a `PedanticRegistry` and matches regexes against the HTTP response body.
- `testCollector` doesn't check the status code. A case with no `matches`, such as `empty`, only catches panics.
- `cmd/` has no tests yet.
- A fake NIS must speak the wire protocol: each message has a 2-byte big-endian length prefix, and a zero-length message ends the response. `apcupsd.New(rwc)` adds this framing to whatever it wraps, so pass it the raw connection.

## Adding a metric

A metric needs four edits in `upscollector.go`: the struct field, the `NewDesc` in `NewUPSCollector`, the list in `Describe`, and the send in `Collect`. Then add a regex to the `full` test case. Without that regex, a mistake that fails the gather still passes the tests.

## Rules for this fork

- **`apcupsd_*` metrics are frozen.** No renames, removals, label changes, or unit changes. The only metric change in `v0.4.0` is the new `apcupsd_output_amps`, which arrives with the upstream merge.
- **Stop before the first push to `main`.** The open PR `upstream #33` uses this fork's `main` as its head branch, so every push adds commits to that PR. Get the maintainer's decision first (issue #1 recommends closing it), and don't close it without that decision.
- **Pushing a branch can publish an image today.** `docker-build.yml` runs after `Linux Test` succeeds on any branch, then pushes `latest` built from the default branch (F6).
- **Never touch upstream.** No PRs, issues, or pushes. `gh` can resolve to the parent repo on a fork, so check with `gh repo set-default --view` and pass `--repo thedebuggedlife/apcupsd_exporter` when in doubt.
- **Don't cross-link upstream.** Write references in backticks, like `upstream #34`. A bare `owner/repo#N` or a URL to an upstream PR adds a backlink to its timeline.
- **Never run `git push --tags`.** Upstream's `0.1.0`, `v0.2.0`, and `v0.3.0` must not be copied here. Push tags by name.
- **Keep the project generic.** Code, docs, commits, PRs, and issues must not refer to a specific deployment, host, network, or person's infrastructure. Use placeholder addresses in examples.
- **Keep the module path** `github.com/mdlayher/apcupsd_exporter`. It has no runtime effect and keeps upstream merges cheap.
- **Keep `upscollector.go` changes small** so future upstream merges stay easy.

## Upstream relationship

The `origin` remote is the fork and `upstream` is `mdlayher/apcupsd_exporter`. Sync with `git merge upstream/main` using a real merge commit, so the next sync has a correct merge base, and do it before bumping dependencies.

Everything fork-specific lives in `Dockerfile`, `.github/workflows/`, `README.md`, and the `-metrics.system` flag in `main.go`. Images are published to `ghcr.io/thedebuggedlife/apcupsd_exporter`.
