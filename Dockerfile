# Build stage — compile the static binary
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/hcdb-server ./cmd/hcdb-server

# Run stage — minimal image, just the binary
FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/hcdb-server .
# data survives container restarts if this is mounted as a volume
VOLUME /app/assets
EXPOSE 6380
ENTRYPOINT ["./hcdb-server"]
