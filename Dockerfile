# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/tomeshift ./cmd/tomeshift

FROM alpine:3.21
RUN adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=builder /out/tomeshift /usr/local/bin/tomeshift
USER appuser

ENTRYPOINT ["/usr/local/bin/tomeshift"]
