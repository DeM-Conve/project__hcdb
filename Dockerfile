# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.24-alpine AS build

# git: some go.mod dependencies are fetched directly from source control.
RUN apk add --no-cache ca-certificates git

WORKDIR /src

# Module files copied separately so dependency downloads are cached
# independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0: static binary, no libc dependency in the run stage.
# -trimpath: no local filesystem paths embedded in the binary.
# -ldflags="-s -w": strip debug symbols and DWARF info.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/hcdb-server \
    ./cmd/hcdb-server

# ---- Run stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
 && addgroup -S hcdb && adduser -S hcdb -G hcdb

WORKDIR /app
COPY --from=build /out/hcdb-server .

ENV HCDB_DATA_DIR=/data
RUN mkdir -p /data && chown -R hcdb:hcdb /data /app

USER hcdb

VOLUME ["/data"]
EXPOSE 6380

# hcdb speaks RESP, not HTTP. An HTTP or plain TCP probe would report a
# healthy server as down: it sends a real RESP-encoded PING and requires
# a PONG reply. nc is provided by BusyBox in the base image.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD printf '*1\r\n$4\r\nPING\r\n' | nc -w 2 127.0.0.1 6380 | grep -q PONG || exit 1

# Exec form so SIGTERM reaches the binary (PID 1) directly.
ENTRYPOINT ["./hcdb-server"]
