# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /hcdb .

# Run stage
FROM alpine:3.19

WORKDIR /app

RUN mkdir -p /app/assets

COPY --from=builder /hcdb .

ENTRYPOINT ["./hcdb"]
