# Use the official Golang image as the build stage
FROM --platform=$BUILDPLATFORM golang:1.21 AS build

# Set environment variables to build a static binary
ARG TARGETOS
ARG TARGETARCH
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
RUN go build -o service ./cmd/apcupsd_exporter/main.go

# Use a minimal base image for the final stage
FROM gcr.io/distroless/static:nonroot

# Set the working directory in the container
WORKDIR /

# Copy the compiled binary from the build stage
COPY --from=build /app/service .

# Run the service binary as the container entrypoint
ENTRYPOINT ["/service"]