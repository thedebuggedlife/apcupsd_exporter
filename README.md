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
  -apcupsd.addr host.docker.internal:3551
```

Using Docker Compose:

```yaml
services:
  apcupsd-exporter:
    image: ghcr.io/thedebuggedlife/apcupsd_exporter:latest
    restart: unless-stopped
    command:
      - -apcupsd.addr host.docker.internal:3551
    ports:
      - 9162:9162
    extra_hosts:
      - host.docker.internal:host-gateway
```