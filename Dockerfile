# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/epub-translator ./cmd/epub-translator

FROM alpine:3.21
RUN adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=builder /out/epub-translator /usr/local/bin/epub-translator
USER appuser

ENTRYPOINT ["/usr/local/bin/epub-translator"]
