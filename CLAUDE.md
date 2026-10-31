# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Prometheus exporter for the [apcupsd](http://www.apcupsd.org/) Network Information Server (NIS), maintained as a fork of `mdlayher/apcupsd_exporter`.

**Modernization in progress.** GitHub issue #1 is the spec for `v0.4.0`, the fork's first tagged release. Read it before starting any step: `gh issue view 1 --repo thedebuggedlife/apcupsd_exporter`. Its steps must be done in order, and its findings are referenced below as F1, F2, and so on. This file describes the code as it stands today, so update it in the same change that makes any of it stale. Step 6 of the issue moves this content into `AGENTS.md`, with `CLAUDE.md` importing it.

## Commands

```sh
go build ./...                                  # compile check, writes no binary
go test -race ./...                             # what CI runs
go test -run 'TestUPSCollector/full' -v .       # single table case in the root package
go vet ./...
staticcheck ./...                               # CI installs honnef.co/go/tools/cmd/staticcheck@latest
gofmt -l .                                      # must print nothing; not in CI yet
go run ./cmd/apcupsd_exporter -apcupsd.addr=127.0.0.1:3551
go run ./internal/fakenis/cmd/fakenis -response full  # fake NIS on 127.0.0.1:3551; -addr changes it
docker build -t apcupsd_exporter .
```

There is no Makefile.

You can smoke-test without a UPS. With an unreachable `-apcupsd.addr`, the exporter starts normally and `/metrics` returns 500. Against the fake NIS, `/metrics` returns 200.

## Architecture

Three layers, each constructing the next:

1. `cmd/apcupsd_exporter/main.go` parses flags, validates `-telemetry.path`, and serves HTTP. `newHandler` registers an `Exporter` on a private Prometheus registry and routes on a private `ServeMux`. `newServer` sets the HTTP timeouts. `newClient` supplies the `ClientFunc` that dials the NIS.
2. `apcupsdexporter.go`: `Exporter` is stateless. Every `Collect` creates a 5s context, dials a new connection, builds a new `UPSCollector`, runs it, and closes the connection. Nothing is cached between scrapes.
3. `upscollector.go`: `UPSCollector` turns one `apcupsd.Status` into const metrics. It reads through the `StatusSource` interface, which is also the test seam (`testStatusSource`).

Behaviour that spans these layers:

- **Failure is all-or-nothing.** A dial or `Status()` error becomes `prometheus.NewInvalidMetric`, which fails the gather, so promhttp returns 500 for the whole page, including `go_*` and `process_*`. Prometheus records this as `up == 0`. `NewUPSCollector(nil)` exists only to supply `Desc`s, for `Exporter.Describe` and for that error, and a nil-source collector must never have `Collect` called.
- **One bad field fails the scrape.** The library's `Status()` returns an error if any single NIS field fails to parse.
- **Unreported fields read as 0.** `apcupsd.Status` is a plain struct, so a field the UPS doesn't report can't be told apart from a real zero. The one exception is `apcupsd_output_amps`, which is omitted unless it is above 0, so a true 0 A reading is omitted too.
- **`Exporter.Describe` is static.** It sends descriptors without touching the network, so registration never blocks on the NIS and the collector is always checked.
- **The 5s timeout covers the whole NIS exchange.** `newClient` copies the context deadline onto the connection, so the same 5s bounds the dial and `Client.Status()`. That deadline is the only thing that frees a stuck `Status()`: the library holds a lock while reading, so `Client.Close()` blocks behind it instead of interrupting it. Any `ClientFunc` must set a connection deadline.
- **`/` is a catch-all 301 to the metrics path.** Any route not explicitly registered redirects there, so a redirect-following client ends up on `/metrics`, which dials apcupsd. `validateMetricsPath` rejects `/` and `/healthz` at startup, because they would collide with a route, and anything the mux can't serve as a plain literal path. A new route must be added to its reserved list.
- **`-metrics.system=false` leaves only `apcupsd_*`.** The go and process collectors and the promhttp instrumentation are added only when the flag is true, and then match what the default registry exposes. `HandlerOpts.Registry` stays unset, because it adds `promhttp_metric_handler_errors_total`.
- **A failed gather is logged.** `HandlerOpts.ErrorLog` logs what promhttp turns into a 500.
- **The write timeout must outlast the scrape limit.** `WriteTimeout` is 15s against the 5s limit; below that, a slow scrape gets a dropped connection instead of a 500.

## Testing notes

- The root package's collector tests scrape through `testCollector`, which uses a `PedanticRegistry` and fails unless the response is 200. Tests that expect a failed scrape use `testCollectorStatus`.
- A table case with no `matches`, such as `empty`, only proves the gather succeeds.
- `TestOutputBaseline` runs raw NIS text through the fake NIS, the real client, the `Exporter` and promhttp, and compares the whole response with `testdata/golden/`. Those files came from the `v0.3.0` binary (`3f531e59`), with the `apcupsd_output_amps` lines added by hand. Never regenerate them from the working tree: a difference there means a frozen metric changed.
- `cmd/apcupsd_exporter/main_test.go` tests the binary's wiring with small local NIS listeners. `TestScrapeFailsWhenNISNeverReplies` takes the full 5s by design.
- `client_golang` exposes no `process_*` families on macOS, so a test may only require them on Linux.
- `internal/fakenis` is the fake NIS, in-process through `fakenis.Listen` or standalone through `internal/fakenis/cmd/fakenis`. Canned responses are the files in `internal/fakenis/responses/`, chosen by name. Any new fake must speak the same wire protocol: each message has a 2-byte big-endian length prefix, and a zero-length message ends the response. `apcupsd.New(rwc)` adds this framing to whatever it wraps, so pass it the raw connection.
- The `oversized` response panics the real client (O3), which crashes a test binary until step 3's `client_golang` upgrade. Until then, read it with raw framing only.

## Adding a metric

A metric needs four edits in `upscollector.go`: the struct field, the `NewDesc` in `NewUPSCollector`, the list in `Describe`, and the send in `Collect`. Then add a regex to the `full` test case, the name to `TestExporterDescribeDoesNotDial`, and the metric's lines by hand to the golden files.

## Rules for this fork

- **`apcupsd_*` metrics are frozen.** No renames, removals, label changes, or unit changes. The only metric change in `v0.4.0` is the new `apcupsd_output_amps`, which arrives with the upstream merge.
- **Keep the `Docker Build` workflow disabled until step 5 replaces it.** While enabled, it runs after `Linux Test` succeeds on any branch, then pushes `latest` built from the default branch (F6). No images publish in the meantime.
- **Never touch upstream.** No PRs, issues, or pushes. `gh` can resolve to the parent repo on a fork, so check with `gh repo set-default --view` and pass `--repo thedebuggedlife/apcupsd_exporter` when in doubt.
- **Don't cross-link upstream.** Write references in backticks, like `upstream #34`. A bare `owner/repo#N` or a URL to an upstream PR adds a backlink to its timeline.
- **Never run `git push --tags`.** Upstream's `0.1.0`, `v0.2.0`, and `v0.3.0` must not be copied here. Push tags by name.
- **Keep the project generic.** Code, docs, commits, PRs, and issues must not refer to a specific deployment, host, network, or person's infrastructure. Use placeholder addresses in examples.
- **Keep the module path** `github.com/mdlayher/apcupsd_exporter`. It has no runtime effect and keeps upstream merges cheap.
- **Keep `upscollector.go` changes small** so future upstream merges stay easy.

## Upstream relationship

The `origin` remote is the fork and `upstream` is `mdlayher/apcupsd_exporter`. Sync with `git merge upstream/main` using a real merge commit, so the next sync has a correct merge base, and do it before bumping dependencies.

Everything fork-specific lives in `Dockerfile`, `.github/workflows/`, `README.md`, `cmd/apcupsd_exporter/`, `internal/fakenis/`, `testdata/`, the static `Exporter.Describe`, the `apcupsd_output_amps` omission in `upscollector.go`, and the agent tooling (`CLAUDE.md`, `.claude/settings.json`, `paseo.json`, `scripts/lumen-prune.sh`, `.gitignore`). Images are published to `ghcr.io/thedebuggedlife/apcupsd_exporter`.

## Agent workflow (superpowers + Paseo)

These are declared preferences; they override the superpowers skills' defaults.

- **Worktrees are owned by Paseo** (`paseo.json` runs `go mod download` on creation; they live under `~/.paseo/worktrees/`). If already in a linked worktree, work there. Never use `EnterWorktree` or `git worktree add`. From the main checkout, create a Paseo worktree (`paseo worktree create --mode branch-off --new-branch <name>`, or `mcp__paseo__create_workspace`), because the main checkout may be shared with other agents. Don't run `go get` or `go mod tidy` in a worktree unless the work is a dependency update; if dependencies are missing, run `go mod download`. After removing a worktree outside Paseo, run `scripts/lumen-prune.sh`.
- **Baseline / verification** is `go test -race ./...`, `go vet ./...`, and an empty `gofmt -l .`.
- **Finishing a branch:** always push and open a PR on the fork with `gh pr create --repo thedebuggedlife/apcupsd_exporter --base main`; never merge into `main` locally. An upstream sync PR must land with a merge commit, never squash or rebase, or the next sync loses its merge base. Archive the worktree through Paseo, then delete the merged branch.
- **Specs** go in `docs/specs/YYYY-MM-DD-<feature>.md`, committed as `docs:` on the feature branch. Issue #1 is the spec for `v0.4.0`, so don't write a second one for that work. Specs are public, so the generic-project rule applies to them.
- **Plans** and other agent workflow artifacts **never** get committed into the tree.
