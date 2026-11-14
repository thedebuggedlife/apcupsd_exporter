# Use the official Golang image as the build stage
FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9 AS build

# Set environment variables to build a static binary
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0 \
    GOOS=$TARGETOS \
    GOARCH=$TARGETARCH

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum, then download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code to the container
COPY . .

# Build the application from the correct entry point
RUN go build -trimpath -ldflags="-s -w -X github.com/mdlayher/apcupsd_exporter.Version=${VERSION}" -o service ./cmd/apcupsd_exporter

# Use a minimal base image for the final stage
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931

# Set the working directory in the container
WORKDIR /

# Copy the compiled binary from the build stage
COPY --from=build /app/service .

# Probe with the binary itself, because the image has no shell or curl
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/service", "-healthcheck"]

# Run the service binary as the container entrypoint
ENTRYPOINT ["/service"]