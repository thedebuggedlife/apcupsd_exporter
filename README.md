# apcupsd_exporter
[![CI](https://github.com/thedebuggedlife/apcupsd_exporter/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/thedebuggedlife/apcupsd_exporter/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/mdlayher/apcupsd_exporter.svg)](https://pkg.go.dev/github.com/mdlayher/apcupsd_exporter)

Command `apcupsd_exporter` provides a Prometheus exporter for the
[apcupsd](http://www.apcupsd.org/) Network Information Server (NIS). MIT
Licensed.

This is a maintained fork of
[mdlayher/apcupsd_exporter](https://github.com/mdlayher/apcupsd_exporter) that
publishes versioned container images. It is a drop-in replacement: it keeps
upstream's `apcupsd_*` metric names, labels and units, and its module path.

## Differences from upstream

- **Container image:** multi-arch distroless images for `linux/amd64`,
  `linux/arm` and `linux/arm64` on GHCR, with [version tags](#image-tags).
- **Healthcheck:** a [`/healthz` endpoint and `-healthcheck` flag](#healthcheck),
  used by the image's `HEALTHCHECK`.
- **`apcupsd_output_amps`:** omitted when the UPS doesn't report it, instead of a
  constant 0. The [metric catalogue](docs/metrics.md) has the details.
- **`-metrics.system=false`:** leaves only the `apcupsd_*` metrics.
- **Bounded scrapes:** the 5 second limit covers the whole NIS exchange, so a NIS
  that accepts a connection and never answers fails the scrape instead of hanging
  it. The HTTP server has timeouts too.
- **Startup:** the exporter doesn't contact apcupsd until the first scrape, and an
  invalid `-telemetry.path` is rejected with a clear error.
- **Dependencies:** current Go and `client_golang`, kept up to date with Dependabot.
  Because of this, the exporter survives an oversized NIS message, and the `go_*`
  families are a smaller set.

## Metrics

[docs/metrics.md](docs/metrics.md) lists every metric with its type, unit, labels
and source NIS field, and what happens when the UPS doesn't report it.

When apcupsd can't be reached, `/metrics` returns an HTTP 500 and Prometheus
records `up == 0`.

## Usage

Available flags for `apcupsd_exporter` include:

```
$ ./apcupsd_exporter -h
Usage of ./apcupsd_exporter:
  -apcupsd.addr string
    	address of apcupsd Network Information Server (NIS) (default ":3551")
  -apcupsd.network string
    	network of apcupsd Network Information Server (NIS): typically "tcp", "tcp4", or "tcp6" (default "tcp")
  -healthcheck
    	probe the exporter on '-telemetry.addr' and exit: 0 if it is serving, 1 if not
  -metrics.system
    	enable or disable default go, process, and promhttp metrics (default true)
  -telemetry.addr string
    	address for apcupsd exporter (default ":9162")
  -telemetry.path string
    	URL path for surfacing collected metrics (default "/metrics")
```

## Running as Docker Container

Using Docker Run:

```bash
docker run -d \
  -p 9162:9162 \
  --name apcupsd-exporter \
  --add-host=host.docker.internal:host-gateway \
  ghcr.io/thedebuggedlife/apcupsd_exporter:v0.4.0 \
  -apcupsd.addr=host.docker.internal:3551
```

Using Docker Compose:

```yaml
services:
  apcupsd-exporter:
    image: ghcr.io/thedebuggedlife/apcupsd_exporter:v0.4.0
    restart: unless-stopped
    command:
      - -apcupsd.addr=host.docker.internal:3551
    ports:
      - 9162:9162
    extra_hosts:
      - host.docker.internal:host-gateway
```

## Image tags

Images are published to `ghcr.io/thedebuggedlife/apcupsd_exporter`. Pin a
`vX.Y.Z` tag if you want to choose when you upgrade.

Releases are made automatically. A merged fix, feature or dependency update
that passed CI is released with no separate review of the release, and a new
major version waits for a maintainer. Patch releases aren't individually
reviewed, so anyone tracking them, or `latest`, receives dependency and Go
toolchain updates that passed CI with nobody having looked at them.
A maintainer can pause releases by turning off the repository's `AUTO_RELEASE`
setting, for example to ship several related changes together. While it's
paused no new tags appear, so a pinned tag stays as it is and `latest` doesn't
move.

| Tag | What it is |
|---|---|
| `vX.Y.Z` | A release. Published once and not moved; a fix ships as a newer tag |
| `latest` | The most recent release to be published. It moves only when a release is tagged, never on a push to `main` |
| `edge` | The newest build of `main`. It is not a release |
| Full commit SHA | The build of that commit |

Every tag covers all three platforms. The GitHub
[releases](https://github.com/thedebuggedlife/apcupsd_exporter/releases) list what
changed in each version.

## Healthcheck

The exporter serves `/healthz`, which returns 200 and never contacts apcupsd.
The `-healthcheck` flag makes the binary request `/healthz` from its own
listener and exit 0 on a 200 and 1 on anything else, within 3 seconds. The
image has no shell or curl, so its `HEALTHCHECK` uses this flag.

Health means the exporter process is serving. An unreachable apcupsd daemon
does **not** make the container unhealthy, because restarting the exporter
can't fix the daemon. That failure shows up as an HTTP 500 from `/metrics`,
which Prometheus records as `up == 0`.

`HEALTHCHECK` can't see the arguments a container runs with, so the image
always probes the default address. If you set `-telemetry.addr`, override the
healthcheck with the same address, or the container will report `unhealthy`
while the exporter is working:

```yaml
services:
  apcupsd-exporter:
    image: ghcr.io/thedebuggedlife/apcupsd_exporter:v0.4.0
    restart: unless-stopped
    command:
      - -apcupsd.addr=host.docker.internal:3551
      - -telemetry.addr=:9200
    healthcheck:
      test: ["CMD", "/service", "-healthcheck", "-telemetry.addr=:9200"]
    ports:
      - 9200:9200
    extra_hosts:
      - host.docker.internal:host-gateway
```
