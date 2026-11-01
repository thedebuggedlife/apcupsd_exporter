# Automated releases

Covers items 1, 2 and 3 of #11. Where this document differs from the issue, this document is newer and wins. Items 4, 5 and 6 are out of scope.

## Why

A change that has merged has already passed CI and been approved, either by the auto-merge policy or by a maintainer. Today it still reaches users only when someone remembers to push a tag. The maintainer wants anything worth releasing to reach users on its own, and doesn't want to review Go patch releases at all.

## Goals

- Every merged fix, feature or dependency update is released automatically once CI passes.
- Go toolchain updates arrive without manual edits. Go patches go all the way to a release with no human involved.
- Nothing can sit unreleased unnoticed.
- The scanner never runs in a job that can write to the registry.

## Non-goals

- Automating a move to a new major version. Those stay held for a maintainer.
- Rollback. Tags are never moved, so a bad release is fixed by a newer one.
- Batching. To land several related PRs as one release, the maintainer pauses the automation.

## Anti-goals

These would make it a failure even if the rest worked:

- A tag or image pushed by a test.
- An `apcupsd_*` change shipped by a path that skipped `TestOutputBaseline`.
- The app key or an app token present in a job that runs the exporter image, a scanner or PR code.
- Moving or deleting a tag.
- Another step a human has to remember.

## Decisions

Made by the maintainer:

1. Go patch PRs merge and release with no human review, gated only by CI. This overrides the issue's "these PRs get human review".
2. Go minor PRs open automatically and CI must pass, then the maintainer merges them. That merge is the only human step.
3. Every merged change that's worth releasing is released. This replaces the issue's rule that every commit must be an auto-merged Dependabot update with at least one security fix, and its rule that a tag is always a patch.
4. Commit titles pick the version.
5. Releases go through release-please, with its release PR auto-merged through the `automated-releases` app. This replaces the issue's scheduled tagging workflow, and matches the maintainer's other repositories.

## Item 1: scan in a read-only job

As written in #11, and agreed:

- Add a `scan` job to `ci.yml` with the workflow's default read-only permissions and the same `if:` as `publish`. It checks out with `persist-credentials: false`, builds `linux/amd64`, and runs the Trivy step with `scanners`, `severity`, `ignore-unfixed` and `exit-code` unchanged.
- `publish` adds `scan` to `needs` and loses its own scan build and scan. Everything else stays, including both tag guards before the login step.
- `scan` runs alongside `test`, `lint` and `vuln`, not after them.
- `image` doesn't change. `scan` isn't a required check.

**Proof:** repeat the #7 procedure on throwaway branches with `push: false` and with the login step and `packages: write` removed. A vulnerable base image fails `scan` and skips `publish`. The positive control reaches `publish`. Delete the branches afterwards.

## Item 2: Go toolchain updates

Dependabot can't do this: it doesn't manage the `go` line here, and its `docker` updater won't touch `go.mod`. A new workflow, `.github/workflows/go-toolchain.yml`, does.

**Schedule:** daily, plus `workflow_dispatch` with a `dry_run` input that defaults to true and reports the planned changes without pushing.

**Targets:** read the current version from `go.mod` and the stable releases from `https://go.dev/dl/?mode=json`.

- The patch target is the newest stable release on the current `X.Y`, if newer than the current version.
- The minor target is the newest stable release with a greater `X.Y`.
- Both can exist at once, and each gets its own PR, so a minor that fails CI can't hold back a patch.

**Waiting for the image:** a target counts only once `golang:<version>-trixie` is published. Until then the run does nothing and says so. This lag is normal, so it isn't a failure. Only a missing tag counts as the lag. Any other failure to read the image, such as a network error, fails the run, so it can't silently stop Go updates.

**Each PR:**

- Sets the `go` line in `go.mod`.
- Sets the `golang` image in the `Dockerfile` to the new tag plus its multi-arch index digest.
- Refreshes the distroless image's multi-arch index digest, on the same tag.
- Lives on a fixed branch, `automation/go-patch` or `automation/go-minor`, built from `main` as a single commit. A newer target force-pushes the same branch, so there's only ever one PR of each kind.
- Is rebuilt from `main` if GitHub reports it as conflicted. This will be routine, because the two Go PRs and Dependabot's digest PRs edit the same `Dockerfile` lines. Otherwise, a run with nothing new changes nothing, so CI isn't re-run each time `main` moves.
- Is titled `fix(deps): bump Go to X.Y.Z`, so both kinds release as a patch.

**Patch PRs** turn on auto-merge with `gh pr merge --auto --squash`, which waits for `test`, `lint`, `vuln` and `image`. A patch PR that gets stuck raises the [reminder](#reminder). **Minor PRs** don't turn on auto-merge.

**Credentials:** the PR is opened with an `automated-releases` app token, because a PR opened with `GITHUB_TOKEN` starts no CI, so required checks would never report. The token is created only after all edits are done, and no new third-party action receives it. The checkout uses `persist-credentials: false`. The token needs `contents: write`, `pull-requests: write` and `workflows: write`. The last is because the fixed branches survive a merge, so a later force-push can carry them across workflow changes on `main`, which GitHub may refuse without it. That failure would only appear on a later Go release, and it's the same permission `dependabot-auto-merge.yml` already holds.

**Dependabot:** add an `ignore` for `golang` with the three `version-update:semver-*` types in the `docker` block, so it stops opening a tag bump that would break the one-Go-version rule. Its docs don't say whether this also stops digest-only updates of `golang`. If it does, the pin still refreshes on each Go release, which is acceptable.

## Item 3: automatic releases

### How a release happens

1. A commit lands on `main`. `.github/workflows/release-please.yml` runs on the push, and release-please opens or updates a release PR, if the commits since the last release call for one.
2. The workflow turns on auto-merge for that PR, if the [hold rules](#hold-rules) allow it.
3. `test`, `lint`, `vuln` and `image` run on the release PR, because the app opened it. `image` runs `scripts/test-container.sh` and Trivy, so the issue's CI and container conditions are both met by required checks before the merge. The workflow doesn't pull the `edge` image.
4. The PR merges, and the push to `main` runs the workflow again. Release-please creates the `vX.Y.Z` GitHub Release, which creates the tag on that commit.
5. The tag was created with the app token, so it fires `on: push`, and `ci.yml` runs `test`, `lint`, `vuln`, `scan` and `publish`, with both tag guards. `ci.yml` keeps its `on: push` tag trigger.

A release therefore follows a merge within minutes, with no scheduled tagging.

### What triggers a release

Release-please reads the commit titles since the last release.

| Commit title | Effect |
|---|---|
| `feat:` or `feat(scope):` | Next minor |
| `fix:`, `perf:` or `revert:`, with or without a scope | Next patch |
| `!` before the colon, or a `BREAKING CHANGE` footer | Next major, `1.0.0` from here, which is [held](#hold-rules) |
| `docs:`, `test:`, `ci:`, `chore:`, `build:`, `refactor:`, `style:` | No release. They go out with the next one |
| A title that isn't a conventional commit, including a merge commit | No release |

`Release-As: X.Y.Z` in a commit body forces a version.

Those are release-please's defaults for its `go` release type, except that `feat` stays a minor below `1.0.0` because neither `bump-minor-pre-major` nor `bump-patch-for-minor-pre-major` is set. Item 4 of #11 needs that, because new metrics go in a minor release.

`exclude-paths` drops commits that only touch `.github`, `docs`, `scripts`, `testdata` or `internal/fakenis`, whatever their title. The option matches directories only, so it can't list a single file like `README.md`.

**Dependabot titles:** Dependabot's plain `Bump x` title isn't a conventional commit, so it would never release. Set `commit-message.prefix` in `.github/dependabot.yml`:

| Ecosystem | Prefix | Effect |
|---|---|---|
| `gomod` | `fix(deps)` | Next patch |
| `docker` | `fix(deps)` | Next patch |
| `github-actions` | `ci(deps)` | No release, because Actions aren't in the image |

The existing digest title check in `dependabot-auto-merge.yml` already accepts a prefix, and `ci.yml`'s `publish` is unaffected. Update the commit style bullet in `AGENTS.md`.

### Configuration

- `release-please-config.json`: `release-type: go`, `include-v-in-tag: true`, `include-component-in-tag: false`, `changelog-path: CHANGELOG.md`, the `exclude-paths` above, and `bootstrap-sha` set to the `v0.4.0` commit, `c225ab894041ff4e4f92a39ffcf024b12e3f211e`. `go` needs no version file, where `simple` would add `version.txt`.
- `.release-please-manifest.json`: `{ ".": "0.4.0" }`.
- `.dockerignore`: add the two JSON files. `CHANGELOG.md` is already covered by `*.md`.
- The first release PR adds `CHANGELOG.md`. Only `v0.4.0` onwards is covered.

### Workflow

`.github/workflows/release-please.yml` is separate from `ci.yml`, so that the app key stays away from `publish`. Its pinned action is the one in the maintainer's other repositories, `googleapis/release-please-action`, updated by Dependabot's `github-actions` block.

**Triggers:** `push` to `main`, `workflow_dispatch` so that changing `AUTO_RELEASE` can take effect without a new commit, and a daily `schedule` that runs only the `remind` job.

| Job | Runs on | Permissions | Does |
|---|---|---|---|
| `release-please` | `push`, `workflow_dispatch` | App token with `contents: write`, `pull-requests: write`, `workflows: write`. No checkout | Runs the action, then applies the hold rules to the open release PR |
| `remind` | `schedule`, `workflow_dispatch` | `GITHUB_TOKEN`, `issues: write`, `actions: read`, `pull-requests: read`. No checkout | Opens, updates or closes the reminder |

`release-please` checks the credentials first, as `dependabot-auto-merge.yml` does. It uses `concurrency` without cancelling, because a cancelled run could leave a merged release PR untagged. The token needs `workflows: write` for the same reason as in item 2: the release branch gets force-updated from `main`.

The action only receives the token, never the private key. It's one more third-party action holding write access to the repository, and the `cooldown` is the only delay before an update of it runs with that access.

### Hold rules

After the action, the job finds the open PR labelled `autorelease: pending` itself, rather than relying on `prs_created`, so a `workflow_dispatch` run can act on a PR nothing has touched. It then decides:

| State | Action |
|---|---|
| `AUTO_RELEASE` is `on`, and the version in the PR title has the same major as the manifest | `gh pr merge --auto --squash` |
| Anything else, including an unparsable title | Turn auto-merge off if it's on |

The check is deliberately fail-closed: an unset `AUTO_RELEASE` holds, and so does a title that doesn't parse. A held PR is the signal. The maintainer can merge it by hand, and that's how a major goes out.

`AUTO_RELEASE` stops releases without a code change. It pauses a batch too: land the related PRs, then set it back to `on` and run the workflow.

### The `release` job

`ci.yml`'s `release` job is removed, because release-please now creates the GitHub Release and `gh release create` would fail on an existing one. A GitHub Release therefore appears shortly before its image does, unlike today, and the reminder covers a publish that fails. Every sibling repository accepts the same ordering.

Hand tags are retired, because a hand tag would get no GitHub Release and release-please wouldn't take it as the new version. `Release-As` replaces them, including for an upstream sync that needs its own release.

### Upstream syncs

A sync lands as a merge commit, which triggers nothing by itself. The maintainer reviewed it by merging it, so its changes go out with the next release, or at once with `Release-As`. The sync's own commits are read too, so an upstream `feat:` or `fix:` can trigger a release and appear in the changelog. That's acceptable noise.

### Reminder

The `remind` job keeps at most one open issue, found by a `release-blocked` label, which the job creates if missing. It's opened or updated when any of these is true:

- An `autorelease: pending` PR has been open for over 24 hours. That covers failed checks, a paused automation and a held major.
- An `automation/go-patch` PR has been open for over 48 hours.
- The newest GitHub Release is over 2 hours old and its tag's `publish` check run didn't succeed.
- A merged PR still carries `autorelease: pending` an hour after merging. Its Release was never created, and release-please won't open another release PR until that's resolved. Re-running `release-please` clears it.
- The latest `push` run of `release-please.yml`, or the latest run of `go-toolchain.yml`, failed. An expired app key would show up here, when nothing else would, because no PR would ever appear.

A waiting Go minor PR doesn't count. It's waiting for the maintainer by design, and the open PR is its own notice.

The body lists the reasons and links to each item. The job edits the issue only if the body changed, so a daily run sends no repeat notifications. When none of these is true, it closes the issue with a comment.

The issue is opened by `github-actions[bot]`, which notifies only people watching the repository, so the job always assigns it to `avargaskun`, the maintainer, both when opening it and when updating it. Any other issue a workflow opens must do the same. The login sits in the workflow rather than in a repository variable, so it can't be left unset and leave the issue unseen. `remind` also needs `actions: read` to read the runs.

The check logic stays inline in the workflow, as `dependabot-auto-merge.yml` does.

## Rollout and proof

No test may push a tag or an image, so no release PR may be merged during testing, and no test may run release-please's `release` command.

1. Land items 1, 2 and 3 in that order, as separate PRs, with `AUTO_RELEASE` unset.
2. Run `actionlint` with shellcheck on the new workflows.
3. Dry-run the config: on a throwaway branch carrying the config, run the release-please CLI's `release-pr --dry-run` with that branch as the target, and confirm the outcome for a `fix:`, a `feat:`, a `feat!:`, a `docs:`, a `fix(ci):` touching only `.github`, and a Dependabot-style `fix(deps):` title. Pin the CLI to the version the action bundles.
4. Exercise the hold rules against recorded inputs, with a `gh` shim, for each row of the table including an unparsable title.
5. Run `remind` on a real runner through `workflow_dispatch`, with a throwaway PR carrying the label, and confirm it opens, updates once and closes one issue, and that the assignee is set. Exercise the merged-PR and failed-run conditions against recorded inputs, with a `gh` shim.
6. Run `go-toolchain.yml` as a dry run on a throwaway branch with an older `go` line and base image, and confirm the planned edits. Build that branch's result once with `push: false`.
7. The maintainer sets the repository's `squash_merge_commit_title` to `PR_TITLE`. It's `COMMIT_OR_PR_TITLE` today, which ignores the PR title on a single-commit PR, so a PR retitled `feat:` during review would release as a patch. Do this as soon as item 3 lands.
8. The next `fix:`, `feat:` or `fix(deps):` commit on `main` opens a real release PR. Check it, and that CI ran on it. It stays open, because `AUTO_RELEASE` is unset.
9. The maintainer sets `AUTO_RELEASE=on` and runs the workflow. This is a deliberate step, isn't part of any PR, and is the first real release.

Only that first release can prove these:

- That a tag created by a GitHub Release through the API with the app token fires `on: push`. The maintainer's other repositories publish from the same workflow instead, so none of them proves it. If it doesn't fire, add `release: types: [published]` to `ci.yml`, which one of those repositories does.
- That `publish` works with the app as actor.
- That a Go patch PR auto-merges, which depends on the same untested paths as Dependabot auto-merge.
- That the token's three permissions are enough for release-please. It normally runs with everything the app holds. If it fails on labels, add `issues: write` to the token, after checking that the app has it.

## Known risks

- **No checkpoint after merge.** A behaviour change titled `fix:` reaches users on automatic patch updates with nobody checking it again. The title is now part of the release, and the README must tell users that patch releases aren't reviewed individually.
- **A title can silently skip a release.** A PR titled without a conventional prefix never triggers one, and nothing flags it. The change goes out with the next release.
- **More releases.** Dependabot's weekly go-module and digest updates each cause a release, so expect one most weeks.
- **The check of the published image is narrower than the manual flow.** `TestOutputBaseline` and `scripts/test-container.sh` run in CI, but steps 2 and 6 of the manual release flow don't happen.
- **A bad release can't be recalled.** Only a newer one fixes it.
- **Scheduled workflows stop after 60 days without repository activity,** which would also stop the reminder. `AGENTS.md` already covers how to check.

## Documentation

- `AGENTS.md`: rewrite the release flow, including "There is no release-please" and "made by hand", and update the `publish`, `scan` and `release` bullets, the Dependabot limits that say users wait for a manual tag, the app section, and the fork-specific file list. Add the rule that a PR's squash title picks the version.
- `README.md`: state that patch releases aren't individually reviewed, and what `AUTO_RELEASE` does.
- Update #11 once this is approved, because its rules for items 2 and 3 have changed.

## What was rejected

- **A custom tagging script on a schedule.** The maintainer chose release-please, which is already in use across their repositories, for one way of working everywhere.
- **Security classification from Dependabot alerts.** The issue's design needed to work out who merged each commit and whether it was a security fix. Releasing everything worth releasing removes the need.
- **Publishing from the release workflow,** as the sibling repositories do. It would put `packages: write` beside the app key, and it would bypass `ci.yml`'s gates.
- **`draft` releases with `force-tag-creation`,** which would keep the Release hidden until its image exists. It adds a step for a problem the reminder already covers.

## Left to the builder

- The exact cron minutes, and the job layout inside each new workflow.
- Whether `go-toolchain.yml` runs `go mod tidy`, provided the PR passes CI.
- The wording of the reminder and of the PR bodies.
