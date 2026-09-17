# Deployment — running HCDB as a server

Files: `cmd/hcdb-server/main.go`, `Dockerfile`, `docker-compose.yml`, `.env.example`,
`.dockerignore`

## `cmd/hcdb-server` — the binary

```go
addr := getenv("HCDB_ADDR", ":6380")
dataDir := getenv("HCDB_DATA_DIR", "assets")

database, _ := db.Open(config.Config{
    WALPath: filepath.Join(dataDir, "server.wal"),
    SSTDir:  filepath.Join(dataDir, "server-sstables"),
})
defer database.Close()

srv := server.New(database)

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()

srv.Serve(ctx, addr)
```

Configuration is entirely via **environment variables**, not flags or hardcoded paths — the point
is that a container image built once should be configurable at `docker run`/`docker compose up`
time without a rebuild:

| Variable | Default | Controls |
|---|---|---|
| `HCDB_ADDR` | `:6380` | TCP listen address |
| `HCDB_DATA_DIR` | `assets` | directory holding the WAL and SSTable files |

`signal.NotifyContext(..., os.Interrupt, syscall.SIGTERM)` is what turns `Ctrl-C` (`SIGINT`) or a
`docker stop` / orderly rolling-restart (`SIGTERM`) into `ctx` cancellation, which drives
`server.Serve`'s graceful-shutdown path (see [server.md](server.md#graceful-shutdown)) instead of
the process being killed outright once its stop-grace-period expires.

## Docker image (`Dockerfile`)

Two-stage build:

1. **Build stage** — `golang:1.24-alpine`. Module files (`go.mod`/`go.sum`) are copied and
   `go mod download` run *before* the rest of the source, so dependency downloads are cached
   independently of source-code changes — editing a `.go` file doesn't invalidate the module
   download layer. The binary is built with `CGO_ENABLED=0` (static binary, no libc dependency in
   the run stage), `-trimpath` (no local filesystem paths embedded in the binary), and
   `-ldflags="-s -w"` (strip debug symbols and DWARF info, smaller binary).
2. **Run stage** — plain `alpine:3.20`, not `scratch` — `nc` (BusyBox) is needed for the
   healthcheck (see below). A non-root user (`hcdb`) is created and owns `/app` and `/data`;
   `USER hcdb` drops privileges before the entrypoint runs. `HCDB_DATA_DIR` defaults to `/data`
   inside the container, declared as a `VOLUME` so it survives container recreation. Port `6380`
   is `EXPOSE`d. The entrypoint is exec-form (`ENTRYPOINT ["./hcdb-server"]`) specifically so
   `SIGTERM` reaches the Go binary directly as PID 1, rather than being swallowed by an
   intermediate shell — required for the graceful-shutdown path above to ever be triggered by
   `docker stop`.

### Healthcheck: a real RESP `PING`, not HTTP

```dockerfile
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD printf '*1\r\n$4\r\nPING\r\n' | nc -w 2 127.0.0.1 6380 | grep -q PONG || exit 1
```

HCDB speaks RESP over a raw TCP socket, not HTTP — there is no HTTP endpoint to probe. An
HTTP-based or plain-TCP-connect healthcheck was tried first and **confirmed to report a working
server as unhealthy**: connecting successfully (or getting a TCP-level response) doesn't tell you
the RESP command loop is actually alive and answering correctly, and a bare HTTP client speaking
to a RESP-only listener doesn't get anything it can interpret as healthy. The fix is to speak the
protocol the server actually understands: pipe a literal, hand-encoded RESP `PING` command
(`*1\r\n$4\r\nPING\r\n` — see [resp.md](resp.md) for the framing) through `nc` and check for the
literal `PONG` reply. `server`'s `PING` handler never touches the database (see
[server.md](server.md)), so this also correctly reports healthy even while a flush or compaction
has the storage engine's write path busy — it's testing "the RESP server is accepting and
answering commands," which is exactly what a healthcheck should mean here.

## `docker-compose.yml`

```yaml
services:
  hcdb:
    build: .
    ports:
      - "${HCDB_PORT:-6380}:6380"
    environment:
      HCDB_DATA_DIR: "${HCDB_DATA_DIR:-/data}"
    volumes:
      - hcdb-data:${HCDB_DATA_DIR:-/data}
    mem_limit: ${HCDB_MEM_LIMIT:-4g}
    cpus: ${HCDB_CPUS:-2.0}
    logging:
      driver: json-file
      options: { max-size: "10m", max-file: "3" }

  redis-cli:
    image: redis:7-alpine
    profiles: ["tools"]
    entrypoint: ["redis-cli", "-h", "hcdb", "-p", "6380"]
    depends_on: [hcdb]

volumes:
  hcdb-data:
```

### Build and run

```bash
docker compose up --build
```

Starts only the `hcdb` service — data persists in the named volume `hcdb-data` across container
recreation (`restart: unless-stopped`), and the healthcheck above is what compose (and any
orchestrator watching container health) uses to know the server is actually serving.

### Environment overrides (`.env.example`)

`docker compose` reads a `.env` file in the same directory automatically; copy
`.env.example` to `.env` and edit:

| Variable | Default | Controls |
|---|---|---|
| `HCDB_PORT` | `6380` | host port published to `6380` in the container |
| `HCDB_DATA_DIR` | `/data` | data directory *inside* the container (passed through as `HCDB_DATA_DIR` to the binary) |
| `HCDB_MEM_LIMIT` | `4g` | container memory limit (`mem_limit`) |
| `HCDB_CPUS` | `2.0` | container CPU limit (`cpus`) |

### Reaching it with real `redis-cli`

The `redis-cli` service is declared with `profiles: ["tools"]`, which means **it does not start**
with a plain `docker compose up` — it exists purely as an on-demand client, not a long-running
service, and would be pointless (and noisy) running continuously. Invoke it explicitly:

```bash
docker compose --profile tools run --rm redis-cli
docker compose --profile tools run --rm redis-cli SET foo bar
docker compose --profile tools run --rm redis-cli GET foo
```

`--rm` cleans up the one-shot container after the command exits. `depends_on: [hcdb]` means
compose starts (or reuses) the `hcdb` service first if it isn't already running.

### Resource limits and logging

`mem_limit`/`cpus` cap the container's resource use (4 GB / 2 vCPUs by default) — worth raising
for a workload with a large working set, since HCDB's block cache and memtable both live in that
same memory budget (see [cache.md](cache.md), [memtable.md](memtable.md)). Logging uses the
`json-file` driver with rotation capped at `10m` per file, 3 files kept — bounded disk use for
container logs regardless of how long the container runs.

### `.dockerignore`

Excludes `.git`, local data artifacts (`assets/`, `*.wal`, `*.sst`, `.benchdata/`), test output
(`*.test`, `*.out`), and files that don't belong in the build context or the image at all
(`docs/`, `README.md`, `docker-compose.yml`, `Dockerfile` itself) — keeps the build context small
and avoids ever baking local WAL/SSTable state into an image layer.

## Related

- [server.md](server.md) — what's actually listening on the port this section exposes
- [resp.md](resp.md) — the wire format the healthcheck's raw `PING` bytes encode
- [db.md](db.md) — `HCDB_DATA_DIR`'s `WALPath`/`SSTDir`, and what actually lives in the volume
