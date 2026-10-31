# AGENTS.md

Guidance for coding agents working in this repository. `CLAUDE.md` only imports this file, so make every change here.

Prometheus exporter for the [apcupsd](http://www.apcupsd.org/) Network Information Server (NIS), maintained as a fork of `mdlayher/apcupsd_exporter`.

This file describes the code as it stands today, so update it in the same change that makes any of it stale.

## Commands

```sh
go build ./...                                  # compile check, writes no binary
go test -race ./...                             # the CI test job
go test -run 'TestUPSCollector/full' -v .       # single table case in the root package
go vet ./...
staticcheck ./...                               # CI pins the version in ci.yml
govulncheck ./...                               # CI pins the version in ci.yml
gofmt -l .                                      # must print nothing; the CI lint job fails otherwise
go run ./cmd/apcupsd_exporter -apcupsd.addr=127.0.0.1:3551
go run ./cmd/apcupsd_exporter -healthcheck      # probes /healthz on -telemetry.addr, exits 0 or 1
go run ./internal/fakenis/cmd/fakenis -response full  # fake NIS on 127.0.0.1:3551; -addr changes it
docker build -t apcupsd_exporter .
docker buildx build --platform linux/amd64,linux/arm,linux/arm64 .  # needs a docker-container builder or the containerd image store
scripts/test-container.sh <image>               # both healthcheck container tests; needs docker and curl
```

There is no Makefile.

You can smoke-test without a UPS. With an unreachable `-apcupsd.addr`, the exporter starts normally and `/metrics` returns 500. Against the fake NIS, `/metrics` returns 200.

## Architecture

Three layers, each constructing the next:

1. `cmd/apcupsd_exporter/main.go` parses flags, runs `-healthcheck` if set, validates `-telemetry.path`, and serves HTTP. `newHandler` registers an `Exporter` on a private Prometheus registry and routes on a private `ServeMux`. `newServer` sets the HTTP timeouts. `newClient` supplies the `ClientFunc` that dials the NIS.
2. `apcupsdexporter.go`: `Exporter` is stateless. Every `Collect` creates a 5s context, dials a new connection, builds a new `UPSCollector`, runs it, and closes the connection. Nothing is cached between scrapes.
3. `upscollector.go`: `UPSCollector` turns one `apcupsd.Status` into const metrics. It reads through the `StatusSource` interface, which is also the test seam (`testStatusSource`).

Behaviour that spans these layers:

- **Failure is all-or-nothing.** A dial or `Status()` error becomes `prometheus.NewInvalidMetric`, which fails the gather, so promhttp returns 500 for the whole page, including `go_*` and `process_*`. Prometheus records this as `up == 0`. A panic in `Collect` ends the same way, because `client_golang` recovers it. `NewUPSCollector(nil)` exists only to supply `Desc`s, for `Exporter.Describe` and for that error, and a nil-source collector must never have `Collect` called.
- **One bad field fails the scrape.** The library's `Status()` returns an error if any single NIS field fails to parse, including fields no metric uses. The exceptions are `ALARMDEL`, whose parse error the library ignores because the field comes in many formats, and unknown keys. Only timestamps accept `N/A`; on a number or duration field it is a parse error.
- **Unreported fields read as 0.** `apcupsd.Status` is a plain struct, so a field the UPS doesn't report can't be told apart from a real zero. The one exception is `apcupsd_output_amps`, which is omitted unless it is above 0, so a true 0 A reading is omitted too. `docs/metrics.md` has the behaviour per metric.
- **`Exporter.Describe` is static.** It sends descriptors without touching the network, so registration never blocks on the NIS and the collector is always checked.
- **The 5s timeout covers the whole NIS exchange.** `newClient` copies the context deadline onto the connection, so the same 5s bounds the dial and `Client.Status()`. That deadline is the only thing that frees a stuck `Status()`: the library holds a lock while reading, so `Client.Close()` blocks behind it instead of interrupting it. Any `ClientFunc` must set a connection deadline.
- **A NIS message over 256 bytes panics the library.** `Client.Status()` reads each message into a 256-byte buffer and slices it with the length the server sent. `client_golang` recovers the panic, so the scrape returns 500 and the process survives. With v1.12.1 nothing recovered it and the process exited, so don't downgrade `client_golang` below v1.25.0 without checking this again. Bounds-checking the length needs a library change. The recovered panic puts the Go stack trace in the 500 body; `HandlerOpts.DisableErrorMessages` would hide it, but would hide dial errors too.
- **`/` is a catch-all 301 to the metrics path.** Any route not explicitly registered redirects there, so a redirect-following client ends up on `/metrics`, which dials apcupsd. `validateMetricsPath` rejects `/` and `/healthz` at startup, because they would collide with a route, and anything the mux can't serve as a plain literal path. Without that check, a colliding path, `metrics` or `/a b` panics at startup, and `/{x}` becomes a wildcard. A new route must be added to its reserved list.
- **`-metrics.system=false` leaves only `apcupsd_*`.** The go and process collectors and the promhttp instrumentation are added only when the flag is true, and then match what the default registry exposes. `HandlerOpts.Registry` stays unset, because it adds `promhttp_metric_handler_errors_total`.
- **A failed gather is logged.** `HandlerOpts.ErrorLog` logs what promhttp turns into a 500, including a recovered panic, which nothing else logs.
- **Health means the exporter is serving.** `/healthz` returns 200 without touching the registry or the `ClientFunc`, and it adds no metric. An unreachable apcupsd must not make the container unhealthy: restarting the exporter can't fix the daemon, and `docker compose up --wait` must not fail because of it. That failure stays a 500 from `/metrics`.
- **`-healthcheck` runs before everything else.** It comes first after `flag.Parse()`, so it never dials apcupsd and can't fail on unrelated flags. `runHealthcheck` does `GET /healthz` on the `-telemetry.addr` listener, with an empty or wildcard host becoming `127.0.0.1`, and returns 0 only on a 200. Its 3s limit must stay below the Dockerfile's 5s `HEALTHCHECK` timeout.
- **The probe client must not follow redirects or use a proxy.** A binary without `/healthz` answers the probe with the catch-all 301 to the metrics path, and following it would dial apcupsd. A proxy could send the probe somewhere other than this listener.
- **The image's `HEALTHCHECK` can't see runtime arguments.** It always probes the default address, so a non-default `-telemetry.addr` needs a matching healthcheck override, shown in the README.
- **The write timeout must outlast the scrape limit.** `WriteTimeout` is 15s against the 5s limit; below that, a slow scrape gets a dropped connection instead of a 500.

## Testing notes

- The root package's collector tests scrape through `testCollector`, which uses a `PedanticRegistry` and fails unless the response is 200. Tests that expect a failed scrape use `testCollectorStatus`.
- A table case with no `matches`, such as `empty`, only proves the gather succeeds.
- `TestOutputBaseline` runs raw NIS text through the fake NIS, the real client, the `Exporter` and promhttp, and compares the whole response with `testdata/golden/`. Those files came from the `v0.3.0` binary (`3f531e59`), with the `apcupsd_output_amps` lines added by hand. Never regenerate them from the working tree: a difference there means a frozen metric changed.
- `TestMetricsCatalogueMatchesDescribe` takes the names from `Exporter.Describe`. Every described metric needs a table row in `docs/metrics.md`, and every `apcupsd_*` name anywhere in that file must be a described metric, so don't write the project's own name there.
- `cmd/apcupsd_exporter/main_test.go` tests the binary's wiring with small local NIS listeners and the fake NIS. `TestScrapeFailsWhenNISNeverReplies` takes the full 5s and `TestRunHealthcheckTimesOutOnSilentServer` the full 3s, by design. `TestMain` runs the real `main` when `APCUPSD_EXPORTER_TEST_RUN_MAIN` is set, so `TestHealthcheckFlagRunsBeforeStartupChecks` can check flag handling in a subprocess. `TestAcceptedMetricsPathsServeOnRealMux` serves every accepted row of `metricsPathCases`, so a new path needs only a row there.
- `scripts/test-container.sh` runs the image's own `HEALTHCHECK` with shorter timings, in a healthy and a mismatched `-telemetry.addr` container. It must keep working on bash 3.2 and without `timeout`, for macOS.
- `process_*` families depend on the platform (macOS lacks `process_network_*`), so a test may only require them on Linux, and a before-and-after comparison must run on Linux.
- `internal/fakenis` is the fake NIS, in-process through `fakenis.Listen` or standalone through `internal/fakenis/cmd/fakenis`. Canned responses are the files in `internal/fakenis/responses/`, chosen by name. Any new fake must speak the same wire protocol: each message has a 2-byte big-endian length prefix, and a zero-length message ends the response. `apcupsd.New(rwc)` adds this framing to whatever it wraps, so pass it the raw connection.
- The `oversized` response panics the real client. `TestOversizedNISMessageFailsOnlyThatScrape` holds in place that only that scrape fails.

## Adding a metric

A metric needs four edits in `upscollector.go`: the struct field, the `NewDesc` in `NewUPSCollector`, the list in `Describe`, and the send in `Collect`. Then add a regex to the `full` test case, the name to `TestExporterDescribeDoesNotDial`, a row to `docs/metrics.md`, and the metric's lines by hand to the golden files.

## Rules for this fork

- **`apcupsd_*` metrics are frozen.** No renames, removals, label changes, or unit changes, because a rename breaks dashboards silently. `apcupsd_output_amps`, which came with an upstream merge, is the only metric added since `v0.3.0`.
- **`latest` moves only on a release tag.** See [Release flow](#release-flow).
- **Never touch upstream.** No PRs, issues, or pushes.
- **Watch the `gh` default repo.** On a fork, `gh` targets the parent repo by default. Run `gh repo set-default thedebuggedlife/apcupsd_exporter` in any new clone, check with `gh repo set-default --view`, and pass `--repo thedebuggedlife/apcupsd_exporter` when in doubt.
- **Don't cross-link upstream.** Write references in backticks, like `upstream #34`. A bare `owner/repo#N` or a URL to an upstream PR adds a backlink to its timeline. Linking the upstream repository's front page is fine.
- **Never run `git push --tags`.** Upstream's `0.1.0`, `v0.2.0`, and `v0.3.0` must not be copied here. Release by pushing the tag by name.
- **Keep the project generic.** Code, docs, commits, PRs, and issues must not refer to a specific deployment, host, network, or person's infrastructure. Use placeholder addresses in examples.
- **One Go version.** `go.mod` and the `Dockerfile` must name the same release; `ci.yml` reads `go.mod` through `go-version-file`. Both `Dockerfile` base images are pinned to a version plus multi-arch index digest, so bump them together.
- **Keep the module path** `github.com/mdlayher/apcupsd_exporter`. It has no runtime effect and keeps upstream merges cheap.
- **Keep `upscollector.go` changes small** so future upstream merges stay easy.

## Release flow

Releases are tag-driven and made by hand. There is no release-please. Every `vX.Y.Z` image is built from a commit already on `main`.

`ci.yml` publishes to `ghcr.io/thedebuggedlife/apcupsd_exporter` for `linux/amd64`, `linux/arm` and `linux/arm64`:

| Trigger | Image tags | GitHub Release |
|---|---|---|
| Strict `vX.Y.Z` tag on a commit already on `main` | `vX.Y.Z` (keeping the `v`), `latest`, full commit SHA | Created by the `release` job |
| Push to `main` | `edge`, full commit SHA | None |
| Weekly `schedule` | `edge`, full commit SHA | None |

To release:

1. Make sure everything is merged to `main` and CI is green.
2. Run the `edge` image for that commit against the fake NIS, and check it as in the last step below. Tags are never moved, so a bad `vX.Y.Z` can only be fixed by a newer tag.
3. Run `git tag vX.Y.Z && git push origin vX.Y.Z`.
4. Confirm `ghcr.io/thedebuggedlife/apcupsd_exporter:vX.Y.Z` exists for all three platforms, and that `latest` has the same digest.
5. Confirm the GitHub Release exists.
6. Run the published image against a real or fake NIS. Confirm every `apcupsd_*` series from the previous release is still there with the same labels, and that the container reaches `healthy`.

How publishing is gated:

- **`publish` needs `test`, `lint` and `vuln`, and never `image`.** `image` is PR-only, and a skipped job in `needs` skips everything that depends on it, so nothing would ever publish.
- **`publish` scans before it pushes,** so a failed Trivy scan can't leave a new `latest` behind.
- **A tag run has two guards:** the tag must be a strict `vX.Y.Z`, and the tagged commit must be on `main`.
- **`latest` is off unless the run is for a tag.** `metadata-action` runs with `flavor: latest=false` and enables `latest` explicitly, so a `main` push or the schedule can never move it.
- **Every job that writes declares its own `permissions:`.** The organisation caps the default `GITHUB_TOKEN` at read-only, and that can't be changed per repo.

Limits:

- `latest` can move backwards. A tag on an older commit that's on `main` passes both guards, so `latest` follows it. Re-running an old tag's run does the same.
- The weekly schedule can give a full commit SHA tag a new digest, because it re-pushes that tag with a fresh build.

## Dependabot

`.github/dependabot.yml` opens weekly version updates for `gomod`, `github-actions` and `docker`, with minor and patch updates grouped. `.github/workflows/dependabot-auto-merge.yml` enables auto-merge on a narrow set of security updates. It is separate from `ci.yml` so that its write token stays away from `publish`.

- **What auto-merges:** a Dependabot security update to Go modules where every updated module moves at patch or minor level between plain `X.Y.Z` releases.
- **What stays manual:** majors, version updates, `github-actions`, `docker`, pseudo-versions such as `github.com/mdlayher/apcupsd`, prereleases and `+incompatible` versions. The `update-type` output can't enforce this alone: a pseudo-version bump reads as `semver-patch`, and a module with no update type is left out of the result. So the workflow checks each module in `updated-dependencies-json`.
- **How a security update is recognised:** the PR is in the `go-security` group, or it is ungrouped and every module has an open Dependabot alert fixed by the new version. The group alone isn't enough, because Dependabot can open a single-module security update outside its group. The lookup uses the REST alerts API and the job's `vulnerability-alerts: read`, so it needs no stored token. `fetch-metadata`'s own `alert-lookup` can come back empty for a Go module that has an open alert, so it isn't used.
- **Anything unclear does nothing.** Missing metadata or a failed alert lookup fails a step, which leaves auto-merge off. The job isn't a required check, so a failure here doesn't block the PR.
- **Required checks must exist before security updates can open.** With nothing required, `gh pr merge --auto` merges at once instead of waiting. `main` needs `test`, `lint`, `vuln` and `image` as required checks, with no required approvals.
- **Repo settings this depends on:** Dependabot alerts and security updates, which forks start with off, and `allow_auto_merge`.
- **`actionlint` doesn't know `vulnerability-alerts` yet.** Run it with `-ignore 'unknown permission scope "vulnerability-alerts"'`.
- **The weekly `schedule` in `ci.yml` stands in for missed pushes.** A merge made with `GITHUB_TOKEN` isn't expected to trigger `on: push`, so CI wouldn't run on `main` and `edge` wouldn't rebuild. The schedule publishes `edge` and the full commit SHA like a `main` push, never `latest`.

Limits:

- `staticcheck` and `govulncheck` are pinned as `go run` arguments in `ci.yml`, so Dependabot can't update them. They need manual bumps.
- A `docker` PR that moves the `golang` image must also update `go.mod`, under the one-Go-version rule.
- Security updates cover Go modules, not the Go standard library. A standard library advisory shows up as a `vuln` failure and needs a manual Go bump. The weekly run is what catches it when no PRs are open.
- While `vuln` is failing, it blocks auto-merge too.
- An auto-merge only reaches `main`. Users get the fix when someone pushes a `vX.Y.Z` tag, which stays manual.
- GitHub disables scheduled workflows after 60 days without repository activity, and by default on a fork. If the weekly runs stop, check `ci.yml`'s `state` with `gh api repos/thedebuggedlife/apcupsd_exporter/actions/workflows`.

## Upstream relationship

The `origin` remote is the fork and `upstream` is `mdlayher/apcupsd_exporter`. In a new clone, add `upstream` and run `git remote set-url --push upstream no_push`, so a push there fails.

To sync, run `git fetch upstream` and `git merge upstream/main` using a real merge commit, so the next sync has a correct merge base, and do it before bumping dependencies. Then check that `TestOutputBaseline` still passes.

What differs from upstream, and why:

| Difference | Why |
|---|---|
| Multi-arch distroless image on GHCR, a single gated `ci.yml`, Dependabot | Upstream publishes no image |
| The 5s scrape limit covers the whole NIS exchange, not just the dial | A NIS that accepts the connection and never answers would hang the scrape and leak a goroutine per scrape |
| Static `Exporter.Describe` | A dial during registration can block startup for up to 5s, and makes the collector checked or unchecked depending on whether the daemon was up |
| `apcupsd_output_amps` is omitted unless above 0 | The library exposes an unreported field as 0, so a UPS without `OUTCURNT` would show a constant 0 |
| `-metrics.system` and a private registry | Lets a deployment expose only `apcupsd_*`. Unregistering from the default registry left `promhttp_*` behind and was fragile across `client_golang` upgrades |
| `-telemetry.path` is validated at startup | An invalid value was a startup panic or a redirect loop |
| `http.Server` timeouts | `http.ListenAndServe` has none |
| `HandlerOpts.ErrorLog` | A recovered panic would otherwise leave no trace in the logs |
| `/healthz` and `-healthcheck` | The distroless image has no shell or curl, so the binary has to probe itself |
| Current Go and `client_golang` | Upstream's `client_golang` v1.12.1 lets an oversized NIS message crash the process |
| The fake NIS and golden output files | They pin the frozen metrics against the real client and the real text format |

Everything fork-specific lives in `Dockerfile`, `.dockerignore`, `.github/workflows/`, `.github/dependabot.yml`, `README.md`, `docs/metrics.md`, `metricsdoc_test.go`, `cmd/apcupsd_exporter/`, `internal/fakenis/`, `scripts/test-container.sh`, `testdata/`, the static `Exporter.Describe`, the `apcupsd_output_amps` omission in `upscollector.go`, and the agent tooling (`AGENTS.md`, `CLAUDE.md`, `.claude/settings.json`, `paseo.json`, `scripts/lumen-prune.sh`, `.gitignore`). Images are published to `ghcr.io/thedebuggedlife/apcupsd_exporter`.

## Agent workflow (superpowers + Paseo)

These are declared preferences; they override the superpowers skills' defaults.

- **Worktrees are owned by Paseo** (`paseo.json` runs `go mod download` on creation; they live under `~/.paseo/worktrees/`). If already in a linked worktree, work there. Never use `EnterWorktree` or `git worktree add`. From the main checkout, create a Paseo worktree (`paseo worktree create --mode branch-off --new-branch <name>`, or `mcp__paseo__create_workspace`), because the main checkout may be shared with other agents. Don't run `go get` or `go mod tidy` in a worktree unless the work is a dependency update; if dependencies are missing, run `go mod download`. After removing a worktree outside Paseo, run `scripts/lumen-prune.sh`.
- **Baseline / verification** is what the CI `test`, `lint` and `vuln` jobs run: `go test -race ./...`, an empty `gofmt -l .`, `go vet ./...`, `staticcheck ./...` and `govulncheck ./...`.
- **Finishing a branch:** always push and open a PR on the fork with `gh pr create --repo thedebuggedlife/apcupsd_exporter --base main`; never merge into `main` locally. Don't put a closing keyword (`close`, `fix`, `resolve` and their forms) directly before an issue number in PR text, even negated, because GitHub ignores the negation; say `Refs #N` or `Part of #N` instead. An upstream sync PR must land with a merge commit, never squash or rebase, or the next sync loses its merge base. Archive the worktree through Paseo, then delete the merged branch.
- **Specs** go in `docs/specs/YYYY-MM-DD-<feature>.md`, committed as `docs:` on the feature branch. Specs are public, so the generic-project rule applies to them.
- **Plans** and other agent workflow artifacts **never** get committed into the tree.
