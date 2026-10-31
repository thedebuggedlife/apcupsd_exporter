# apcupsd_exporter 
[![Linux Test](https://github.com/thedebuggedlife/apcupsd_exporter/actions/workflows/linux-test.yml/badge.svg?branch=main)](https://github.com/thedebuggedlife/apcupsd_exporter/actions/workflows/linux-test.yml) [![Docker Build](https://github.com/thedebuggedlife/apcupsd_exporter/actions/workflows/docker-build.yml/badge.svg?branch=main)](https://github.com/thedebuggedlife/apcupsd_exporter/actions/workflows/docker-build.yml) [![GoDoc](http://godoc.org/github.com/mdlayher/apcupsd_exporter?status.svg)](http://godoc.org/github.com/mdlayher/apcupsd_exporter)

Command `apcupsd_exporter` provides a Prometheus exporter for the
[apcupsd](http://www.apcupsd.org/) Network Information Server (NIS). MIT
Licensed.

## Usage

Available flags for `apcupsd_exporter` include:

```
$ ./apcupsd_exporter -h
Usage of ./apcupsd_exporter:
  -apcupsd.addr string
        address of apcupsd Network Information Server (NIS) (default ":3551")
  -apcupsd.network string
        network of apcupsd Network Information Server (NIS): typically "tcp", "tcp4", or "tcp6" (default "tcp")
  -telemetry.addr string
        address for apcupsd exporter (default ":9162")
  -telemetry.path string
        URL path for surfacing collected metrics (default "/metrics")
  -metrics.system bool
        set to false to disable emitting system level metrics (default "true")
```

## Running as Docker Container

Using Docker Run:

```bash
docker run -d \
  -p 9162:9162 \
  --name apcupsd-exporter \
  --add-host=host.docker.internal:host-gateway \
  ghcr.io/thedebuggedlife/apcupsd_exporter:latest \
  -apcupsd.addr=host.docker.internal:3551
```

Using Docker Compose:

```yaml
services:
  apcupsd-exporter:
    image: ghcr.io/thedebuggedlife/apcupsd_exporter:latest
    restart: unless-stopped
    command:
      - -apcupsd.addr=host.docker.internal:3551
    ports:
      - 9162:9162
    extra_hosts:
      - host.docker.internal:host-gateway
```

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
    image: ghcr.io/thedebuggedlife/apcupsd_exporter
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
