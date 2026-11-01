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
scripts/test-go-toolchain.sh                    # tests scripts/go-toolchain.sh; needs jq
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
- **`latest` moves only on a release tag,** and tags come only from release-please. See [Release flow](#release-flow).
- **Never touch upstream.** No PRs, issues, or pushes.
- **Watch the `gh` default repo.** On a fork, `gh` targets the parent repo by default. Run `gh repo set-default thedebuggedlife/apcupsd_exporter` in any new clone, check with `gh repo set-default --view`, and pass `--repo thedebuggedlife/apcupsd_exporter` when in doubt.
- **Don't cross-link upstream.** Write references in backticks, like `upstream #34`. A bare `owner/repo#N` or a URL to an upstream PR adds a backlink to its timeline. Linking the upstream repository's front page is fine.
- **Never run `git push --tags`.** Upstream's `0.1.0`, `v0.2.0`, and `v0.3.0` must not be copied here. Never push a tag by hand either: release-please creates the tags, and a hand tag gets no GitHub Release.
- **Keep the project generic.** Code, docs, commits, PRs, and issues must not refer to a specific deployment, host, network, or person's infrastructure. Use placeholder addresses in examples.
- **One Go version.** `go.mod` and the `Dockerfile` must name the same release; `ci.yml` reads `go.mod` through `go-version-file`. Both `Dockerfile` base images are pinned to a version plus multi-arch index digest, so bump them together.
- **Keep the module path** `github.com/mdlayher/apcupsd_exporter`. It has no runtime effect and keeps upstream merges cheap.
- **Keep `upscollector.go` changes small** so future upstream merges stay easy.

## Release flow

Releases are automatic and run through release-please. A change that has merged to `main` and calls for a release is released once CI passes, with nothing for a person to remember. Every `vX.Y.Z` image is built from a commit already on `main`.

How a release happens:

1. A commit lands on `main`. `.github/workflows/release-please.yml` runs on the push, and release-please opens or updates a release PR titled `chore(main): release X.Y.Z`, if the commits since the last release call for one. It labels the PR `autorelease: pending`.
2. The workflow turns on auto-merge for that PR, if the [hold rules](#hold-rules) allow it.
3. `test`, `lint`, `vuln` and `image` run on the release PR, because the app opened it. `image` runs `scripts/test-container.sh` and Trivy. The `edge` image isn't pulled.
4. The PR merges. The push to `main` runs the workflow again, and release-please creates the `vX.Y.Z` GitHub Release, which creates the tag on that commit.
5. The tag was created with the app token, so it fires `on: push` and `ci.yml` publishes.

`ci.yml` publishes to `ghcr.io/thedebuggedlife/apcupsd_exporter` for `linux/amd64`, `linux/arm` and `linux/arm64`:

| Trigger | Image tags | GitHub Release |
|---|---|---|
| Strict `vX.Y.Z` tag on a commit already on `main` | `vX.Y.Z` (keeping the `v`), `latest`, full commit SHA | Created by release-please, shortly before the image exists |
| Push to `main` | `edge`, full commit SHA | None |

`ci.yml` has no `release` job, because release-please creates the Release and `gh release create` would fail on an existing one. A publish that fails after the Release exists is what the [reminder](#the-release-reminder) is for. Tags are never moved, so a bad `vX.Y.Z` can only be fixed by a newer release.

To check a release, confirm that `ghcr.io/thedebuggedlife/apcupsd_exporter:vX.Y.Z` exists for all three platforms and that `latest` has the same digest. Then run it against a real or fake NIS, confirm every `apcupsd_*` series from the previous release is still there with the same labels, and that the container reaches `healthy`.

### What picks the version

A PR's squash title becomes the commit title on `main`, and release-please reads it. It must be a conventional commit, or the change releases nothing by itself. The repository's `squash_merge_commit_title` must be `PR_TITLE`: with the default `COMMIT_OR_PR_TITLE`, a single-commit PR is squashed under its commit's title and a retitled PR would release as the wrong version. The setting can be changed before or after the release PRs land. Keep `squash_merge_commit_message` as `COMMIT_MESSAGES`, because `PR_BODY` would feed Dependabot's quoted release notes into bodies that release-please parses. The title isn't the only input: a body paragraph that starts with a conventional prefix also counts, for example a `docs: x` PR whose body has a paragraph starting `fix: `. A multi-commit squash is safe, because GitHub prefixes its lines with `* `.

| Commit title | Effect |
|---|---|
| `feat:` or `feat(scope):` | Next minor |
| `fix:`, `perf:` or `revert:`, with or without a scope | Next patch |
| `!` before the colon, or a `BREAKING CHANGE` footer | Next major, `1.0.0` from here, which is [held](#hold-rules) |
| `docs:`, `test:`, `ci:`, `chore:`, `build:`, `refactor:`, `style:` | No release. They go out with the next one |
| A title that isn't a conventional commit | No release |
| `Revert "fix: a (#12)"`, which GitHub's Revert button gives a PR | Parses to nothing, so no release follows. Retitle it `revert:` or `fix:` |

- **`Release-As: X.Y.Z` in a commit body forces a version,** as a footer of a conventional paragraph. `Merge pull request #30 …` followed by a blank line and only `Release-As: 0.5.0` gives nothing, silently. It is also how an upstream sync gets its own release.
- **A forced release must touch a file outside `exclude-paths`.** `exclude-paths` drops a commit with no files, so the usual empty `Release-As` commit opens no release PR. `fix(deps): release ...` on a commit that touches a file such as `AGENTS.md` gives the next patch, with no `Release-As` needed.
- **`feat` stays a minor below `1.0.0`,** because neither `bump-minor-pre-major` nor `bump-patch-for-minor-pre-major` is set. New metrics go in a minor release.
- **`exclude-paths`** in `release-please-config.json` drops commits that only touch `.github`, `docs`, `scripts`, `testdata` or `internal/fakenis`, whatever their title. It matches directories only, so it can't list `README.md`.
- **Dependabot titles are prefixed** so they count: `fix(deps)` for `gomod` and `docker`, which releases a patch, and `ci(deps)` for `github-actions`, which doesn't release, because Actions aren't in the image. The Go toolchain PRs are titled `fix(deps): bump Go to X.Y.Z`.
- **Upstream syncs:** the `merge_commit_message` setting is `PR_TITLE`, so a GitHub merge commit reads `Merge pull request #N …`, a blank line, then the PR title. release-please splits on a blank line followed by a conventional prefix, so that title paragraph counts: `fix: sync upstream` would release a patch and `feat:` a minor. Title the sync PR `chore: sync upstream`, so the merge doesn't release by itself and its changes go out with the next release. To release a sync at once, add a `Release-As: X.Y.Z` footer under that title paragraph in the merge dialog. The upstream commits the merge brings are read too, so an upstream `feat:` or `fix:` can trigger a release and appear in the changelog.
- **Config:** `release-please-config.json` (`release-type: go`, `v` in the tag, no component, `bootstrap-sha` at the `v0.4.0` commit) and `.release-please-manifest.json` (the last released version). The first release PR adds `CHANGELOG.md`, which covers only `v0.4.0` onwards. All three are in `.dockerignore` or covered by `*.md`.
- **No hand tags.** A hand tag gets no GitHub Release, and release-please wouldn't take it as the new version. Use `Release-As`.

### Hold rules

After the action runs, the `release-please` job finds the open PR labelled `autorelease: pending` itself, so a `workflow_dispatch` run can act on a PR nothing has touched. The label is matched client-side, because `gh pr list --label` goes through the search index, which may not list a PR created seconds earlier by the same run. The step runs even after the action fails, so turning `AUTO_RELEASE` off still disarms a PR.

| State | Action |
|---|---|
| The repository variable `AUTO_RELEASE` is exactly `on`, and the version in the PR title has the same major as the manifest on `main` | `gh pr merge --auto --squash` |
| Anything else, including a title that doesn't parse and a manifest that can't be read | Turn auto-merge off if it's on |

The check fails closed: an unset `AUTO_RELEASE` holds, and so does `ON`. A held PR is the signal. The maintainer can merge it by hand, and that is how a major goes out.

- **To pause all releases,** set `AUTO_RELEASE` to anything but `on` (or delete it) and run `release-please.yml` from the Actions tab, where it is shown as `Release`, so the change takes effect at once. The release PR stays open and keeps collecting commits. To resume, set it back to `on` and run the workflow. This is also how to batch several PRs into one release.
- **A pause stops releases, not merges.** Dependabot and Go patch PRs still auto-merge into `main`, and the next release picks them up. The hold for a Go patch is separate and still applies: see [Go toolchain updates](#go-toolchain-updates).
- **A pause means no new tags,** so anyone pinned to a tag stays on it and `latest` doesn't move.

### The release reminder

The `remind` job in `release-please.yml` keeps at most one open issue, titled `Automatic release is blocked` and labelled `release-blocked`, which the job creates if missing. It runs daily and on `workflow_dispatch`, with a read-only `GITHUB_TOKEN` plus `issues: write` and `actions: read`. It opens or updates the issue when any of these is true:

- An `autorelease: pending` PR has been open for over 24 hours. That covers failed checks, a paused automation and a held major.
- An `automation/go-patch` PR has been open for over 48 hours. A PR from a fork doesn't count. A waiting Go minor PR doesn't count either, because the open PR is its own notice.
- The newest GitHub Release is over 2 hours old and its tag's own `ci.yml` run has no successful `publish` job. A `publish` run on `main` for the same commit doesn't count.
- A merged PR still carries `autorelease: pending` an hour after merging. Its Release was never created, and release-please won't open another release PR until that is resolved. Re-running `release-please.yml` clears it.
- The latest `push` run of `release-please.yml`, or the latest `schedule` run of `go-toolchain.yml`, ended in `failure`, `timed_out` or `startup_failure`. An expired app key shows up here, when nothing else would. Only a re-run of that failed run, or the next push, clears the `release-please.yml` line, because a dispatch isn't a push run.

The job edits the issue only if the body changed, so a daily run sends no repeat notifications. It closes the issue with a comment when none of this is true. A failed query fails the job and leaves the issue as it was. The issue is opened by `github-actions[bot]`, which notifies only people watching the repository, so the job assigns it to the maintainer's login, which sits in the workflow. Any other issue a workflow opens must do the same.

### How publishing is gated

- **`publish` needs `test`, `lint`, `vuln` and `scan`, and never `image`.** `image` is PR-only, and a skipped job in `needs` skips everything that depends on it, so nothing would ever publish.
- **`scan` runs in its own read-only job and `publish` needs it,** so a failed scan can't leave a new `latest` behind and the scanner never shares a job with `packages: write`.
- **`scan` and `publish` must stay on the same events.** Both `if:` conditions have to accept any event that publishes. If one accepted an event the other skipped, `publish` would be skipped with it, so nothing would publish.
- **A tag run has two guards:** the tag must be a strict `vX.Y.Z`, and the tagged commit must be on `main`.
- **`latest` is off unless the run is for a tag.** `metadata-action` runs with `flavor: latest=false` and enables `latest` explicitly, so a `main` push can never move it.
- **Every job that writes with `GITHUB_TOKEN` declares its own `permissions:`.** The organisation caps the default `GITHUB_TOKEN` at read-only, and that can't be changed per repo.
- **Only a push publishes.** The weekly `schedule` runs `test`, `lint` and `vuln`.
- **The release workflow never publishes.** It is separate from `ci.yml`, so the app key stays away from `publish`, and the image still goes through every `ci.yml` gate.

Limits:

- `latest` can move backwards. A tag on an older commit that's on `main` passes both guards, so `latest` follows it. Re-running an old tag's run does the same.
- Until the first automated release, nothing has proved that a tag created by release-please with the app token fires `on: push`. If it doesn't, add `release: types: [published]` to `ci.yml`, and make both `scan`'s and `publish`'s `if:` accept that event. That fallback won't fire for a Release that already exists, so that version stays without an image and the reminder stays open until a newer Release goes out.
- An armed release PR can merge after a `feat!:` lands but before the run retitles it, shipping a breaking change in a patch tag, and the same window ships any commit without a changelog entry. The window is about one run long, and it is accepted.
- Nobody checks a release after it merges. A behaviour change titled `fix:` reaches users on their next pull, and the README says patch releases aren't individually reviewed.
- Dependabot's weekly updates each cause a release, so expect about one a week.
- Scheduled workflows stop after 60 days without repository activity, which would also stop the reminder.

## Dependabot

`.github/dependabot.yml` opens weekly version updates for `gomod`, `github-actions` and `docker`, with patch and minor updates in separate groups and a 7-day `cooldown`, which security updates ignore. `.github/workflows/dependabot-auto-merge.yml` decides what auto-merges. It is separate from `ci.yml` so that the app key stays away from `publish`.

| Update | Ordinary | Security |
|---|---|---|
| Patch | Auto-merge | Auto-merge |
| Base image digest only (same tag) | Auto-merge | Auto-merge |
| Minor | Held for review | Auto-merge for Go modules, held otherwise |
| Major | Held for review | Held for review |
| Base image moving to a new tag | Held for review | Held for review |
| Unknown update type | Held for review | Held for review |

"Held for review" means the PR stays open with auto-merge off. A required check can stop any of these merges, because auto-merge only ever waits for them.

- **Every update in a PR must qualify.** One minor holds a whole PR, which is why the groups are split by level.
- **Patch and minor mean a move between plain `X.Y.Z` versions.** Pseudo-versions such as `github.com/mdlayher/apcupsd`, prereleases, `+incompatible` versions and bare commit SHAs count as unknown. The `update-type` output can't enforce this alone: a pseudo-version bump reads as `semver-patch`, and a module with no update type is left out of the result. So the workflow checks each module in `updated-dependencies-json`.
- **Any base image tag change is held.** The `golang` tag is the exception: Dependabot ignores it, and the [Go toolchain workflow](#go-toolchain-updates) bumps it together with `go.mod`, as the one-Go-version rule needs.
- **How a digest-only update is recognised:** the PR is `docker`, holds one update, has a title ending in ``from `abc1234` to `def5678` `` (and optionally ` in the <group> group`), and `prevVersion` is empty or a backticked digest. `update-type` can't do this, because real digest PRs show `semver-patch` or nothing. Dependabot prints backticks only when the tag is unchanged. `prevVersion` alone can't do it either: it is also empty for a tag change whose tag doesn't start with a digit, such as `nonroot`. The title carries the `commit-message.prefix` from `dependabot.yml`, as in `fix(deps): Bump x from ...`. Dependabot writes `Bump` with a capital B because earlier commits in the repository are capitalised, not because of the prefix, and the check accepts either case.
- **Digest updates stay out of groups.** A group with `update-types` admits only updates that compare higher, and a digest bump keeps its version, so it arrives as its own PR. That is why the `docker` groups keep `update-types`. A group PR holding several digests would be held.
- **How a security update is recognised:** a Go module PR in the `go-security` group, or an ungrouped one where every module has an open Dependabot alert fixed by the new version. The group alone isn't enough, because Dependabot can open a single-module security update outside its group. The lookup uses the REST alerts API. `fetch-metadata`'s own `alert-lookup` isn't used: it matches the alert's package name exactly, so it misses a Go alert filed against a package inside the module, and a failed lookup would fail the whole metadata step.
- **An unreadable alert means an ordinary update.** A security patch still merges as a patch, and a security minor is held.
- **A PR with more than one commit is held.** `fetch-metadata` only checks the first commit, so a commit pushed on top of Dependabot's would otherwise inherit its decision.
- **Each push is decided again.** If the decision is to hold and auto-merge is on, the workflow turns it off. That includes auto-merge a maintainer turned on by hand, and a metadata failure. If the app token can't be created, the job fails and can't turn it off.
- **The job isn't a required check,** so a failure in it doesn't block the PR.
- **Required checks must exist before security updates can open.** With nothing required, `gh pr merge --auto` merges at once instead of waiting. `main` needs `test`, `lint`, `vuln` and `image` as required checks, with no required approvals.
- **Repo settings this depends on:** Dependabot alerts and security updates, which forks start with off, `allow_auto_merge`, and an Actions event policy.
- **An Actions event policy keeps `pull_request_target` working.** From 2 December 2026, GitHub blocks that event by default on public repositories with no applicable policy. The repository policy under **Settings > Actions > Policies** allows it for `.github/workflows/dependabot-auto-merge.yml` only. Its `allowed_events` is an allow-list, so never remove its `workflow_path` condition, or it blocks every other event in the repository. A blocked run ends as `startup_failure` with no jobs, and the Dependabot PR stays open.

### The `automated-releases` app

`dependabot-auto-merge.yml`, `go-toolchain.yml` and the `release-please` job of `release-please.yml` do their writing with a token from the `automated-releases` GitHub App. The jobs' own `GITHUB_TOKEN` has no write permissions, except in the `remind` job, which has `issues: write` and no app token.

- **Why an app:** `GITHUB_TOKEN` can't hold `workflows`, so GitHub refuses to enable auto-merge on a PR that edits `.github/workflows/`. And a merge made with `GITHUB_TOKEN` doesn't fire `on: push`, while one made with the app does, so CI and `publish` run for every auto-merge.
- **Credentials:** the repo variable `RELEASE_BOT_APP_ID` and the Actions secret `RELEASE_BOT_PRIVATE_KEY`. Each workflow requests only what it needs: the auto-merge token asks for `contents: write`, `pull-requests: write`, `vulnerability-alerts: read` and `workflows: write`, and the release and Go toolchain tokens ask for `contents: write`, `pull-requests: write` and `workflows: write`. If release-please fails on labels, the release token needs `issues: write` too, after checking that the app has it.
- **Why the release token is an app token:** a tag created by release-please with `GITHUB_TOKEN` fires no `on: push`, so `publish` would never run, and a release PR opened with it starts no CI. The `release-please` job has no checkout and only the action receives the token, never the private key.
- **The `pull_request_target` job must never run PR code.** It holds the app key, so it has no `actions/checkout`, the logic stays inline, and PR-controlled values reach scripts only through `env`. It runs only when the author is `dependabot[bot]` and the head repo is this repo.
- **`pull_request_target` gets Actions secrets on a Dependabot PR,** unless Dependabot created the PR's base branch. GitHub's general statement that Dependabot runs see only Dependabot secrets doesn't apply to this event. If `Check the app credentials` fails on a Dependabot PR, add `RELEASE_BOT_PRIVATE_KEY` as a Dependabot secret too.

Limits:

- `staticcheck` and `govulncheck` are pinned as `go run` arguments in `ci.yml`, so Dependabot can't update them. They need manual bumps.
- Security updates cover Go modules, not the Go standard library. A standard library advisory shows up as a `vuln` failure, and the [Go toolchain workflow](#go-toolchain-updates) fixes it once a Go release is out and its image is published. The weekly `schedule` is what catches it when no PRs are open.
- While `vuln` is failing, it blocks auto-merge too.
- An auto-merge reaches `main` and `edge`. Users get the fix when the next release goes out, which follows from the commit title: `fix(deps)` releases a patch, `ci(deps)` releases nothing, and a paused `AUTO_RELEASE` delays it. See [Release flow](#release-flow).
- `github-actions` patches include the third-party actions in `publish`, and the two actions in the auto-merge job, which holds the app key. `go-toolchain.yml` and `release-please.yml` also hold it, through `create-github-app-token` on a schedule or a push to `main`, and `release-please.yml` hands the token to `googleapis/release-please-action`, the only third-party action that receives it, and to the `gh` step that applies the hold rules. The `cooldown` is the only delay before they run with those credentials.
- The `cooldown` doesn't hold a digest update if the registry gives no publish date.
- GitHub disables scheduled workflows after 60 days without repository activity, and by default on a fork. If the weekly `ci.yml` runs or the daily `go-toolchain.yml` and `release-please.yml` runs stop, check their `state` with `gh api repos/thedebuggedlife/apcupsd_exporter/actions/workflows`.

## Go toolchain updates

Dependabot's `docker` updater can't edit `go.mod`, so `.github/workflows/go-toolchain.yml` moves Go instead. `.github/dependabot.yml` ignores `golang` in its `docker` block. Whether that also stops digest-only updates of `golang` is undocumented; if it does, the pin still refreshes on each Go release.

- **Targets:** `scripts/go-toolchain.sh targets` reads the `go` line in `go.mod` and the stable releases from `https://go.dev/dl/?mode=json`. The patch target is the newest release on the current `X.Y`, and the minor target is the newest with a greater `X.Y`. Both can exist at once, as a matrix of two jobs.
- **Two PRs on fixed branches:** `automation/go-patch` and `automation/go-minor`, each a single commit from `main` titled `fix(deps): bump Go to X.Y.Z`, so both release as a patch. `apply` sets `go.mod`, the `golang` tag and index digest in the `Dockerfile`, and refreshes the distroless digest on the same tag. A newer target force-pushes the same branch.
- **Waiting for the image:** a target counts only once `golang:<version>-trixie` is published. Until then the run notices and stops, which isn't a failure.
- **No change when nothing is new.** An open PR with the right title is left alone (for a patch, once its auto-merge is on), so CI doesn't re-run each day. A PR that GitHub reports as `CONFLICTING` is rebuilt from `main`, which is routine because Dependabot's digest PRs edit the same `Dockerfile` lines. Only a PR from a branch in this repo counts; a fork PR with the same branch name is ignored.
- **Patch PRs get `gh pr merge --auto --squash`** and wait for the required checks. Minor PRs don't, so they need a manual merge.
- **Patch auto-merge is re-armed on every run.** A current patch PR whose auto-merge is off gets only `gh pr merge --auto --squash`, with no rebuild or push. That retries a failed arming and also undoes a maintainer turning it off by hand, deliberately: Go patches always go automatically. To hold one, turn the `Go toolchain` workflow off in the Actions tab first, then run `gh pr merge --disable-auto <n>`, because auto-merge is state on the PR and the next run would re-arm it in the other order. A disarmed PR with green checks merges at once when re-armed, because `gh` merges immediately in the `CLEAN` state.
- **Closing a Go PR brings it back on the next run,** for a patch or a minor. To decline a Go minor, leave its PR open.
- **A rebuild force-pushes the fixed branch** and drops any commit on it, so fixes for a Go PR land on `main` in their own PR.
- **Only a missing `golang` tag counts as waiting for the image.** Any other failure to read an image digest fails the run, so a persistent registry problem is visible instead of silently stopping updates.
- **Dry run:** a `workflow_dispatch` with `dry_run` (the default) prints the commit and stops before the app token is created. A schedule run is never a dry run.
- **No token before the edits are done.** The checkout uses `persist-credentials: false`, and the job's read-only `GITHUB_TOKEN` is used by the checkout, which doesn't persist it, and by `gh pr list`. The app token is created after the commit, and only the push and `gh` steps receive it.
- **Why the app token holds `workflows`:** the fixed branches survive a merge, so a later force-push can carry them across workflow changes on `main`, which GitHub may refuse without it. That failure would appear only on a later Go release.
- **Why an app token at all:** a PR opened with `GITHUB_TOKEN` starts no CI, so the required checks would never report.

## Upstream relationship

The `origin` remote is the fork and `upstream` is `mdlayher/apcupsd_exporter`. In a new clone, add `upstream` and run `git remote set-url --push upstream no_push`, so a push there fails.

To sync, run `git fetch upstream` and `git merge upstream/main` using a real merge commit, so the next sync has a correct merge base, and do it before bumping dependencies. Then check that `TestOutputBaseline` still passes. Title the sync PR `chore: sync upstream`, and see [What picks the version](#what-picks-the-version) for releasing it.

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
| Releases through release-please, and Go toolchain PRs | Upstream releases by hand, so a fix can sit unreleased and the Go version drifts |
| Current Go and `client_golang` | Upstream's `client_golang` v1.12.1 lets an oversized NIS message crash the process |
| The fake NIS and golden output files | They pin the frozen metrics against the real client and the real text format |

Everything fork-specific lives in `Dockerfile`, `.dockerignore`, `.github/workflows/`, `.github/dependabot.yml`, `README.md`, `docs/metrics.md`, `metricsdoc_test.go`, `cmd/apcupsd_exporter/`, `internal/fakenis/`, `docs/specs/`, `release-please-config.json`, `.release-please-manifest.json`, `CHANGELOG.md`, `scripts/test-container.sh`, `scripts/go-toolchain.sh`, `scripts/test-go-toolchain.sh`, `testdata/`, the static `Exporter.Describe`, the `apcupsd_output_amps` omission in `upscollector.go`, and the agent tooling (`AGENTS.md`, `CLAUDE.md`, `.claude/settings.json`, `paseo.json`, `scripts/lumen-prune.sh`, `.gitignore`). Images are published to `ghcr.io/thedebuggedlife/apcupsd_exporter`.

## Agent workflow (superpowers + Paseo)

These are declared preferences; they override the superpowers skills' defaults.

- **Worktrees are owned by Paseo** (`paseo.json` runs `go mod download` on creation; they live under `~/.paseo/worktrees/`). If already in a linked worktree, work there. Never use `EnterWorktree` or `git worktree add`. From the main checkout, create a Paseo worktree (`paseo worktree create --mode branch-off --new-branch <name>`, or `mcp__paseo__create_workspace`), because the main checkout may be shared with other agents. Don't run `go get` or `go mod tidy` in a worktree unless the work is a dependency update; if dependencies are missing, run `go mod download`. After removing a worktree outside Paseo, run `scripts/lumen-prune.sh`.
- **Baseline / verification** is what the CI `test`, `lint` and `vuln` jobs run: `go test -race ./...`, an empty `gofmt -l .`, `go vet ./...`, `staticcheck ./...` and `govulncheck ./...`.
- **Finishing a branch:** always push and open a PR on the fork with `gh pr create --repo thedebuggedlife/apcupsd_exporter --base main`; never merge into `main` locally. Title the PR as a conventional commit, because its squash title picks the version (see [What picks the version](#what-picks-the-version)). Don't put a closing keyword (`close`, `fix`, `resolve` and their forms) directly before an issue number in PR text, even negated, because GitHub ignores the negation; say `Refs #N` or `Part of #N` instead. An upstream sync PR must land with a merge commit, never squash or rebase, or the next sync loses its merge base. Archive the worktree through Paseo, then delete the merged branch.
- **Specs** go in `docs/specs/YYYY-MM-DD-<feature>.md`, committed as `docs:` on the feature branch. Specs are public, so the generic-project rule applies to them.
- **Plans** and other agent workflow artifacts **never** get committed into the tree.
