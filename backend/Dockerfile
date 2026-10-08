# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/noters ./cmd/noters

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S noters && adduser -S -G noters -u 10001 noters
COPY --from=builder /out/noters /usr/local/bin/noters
USER noters
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/noters"]
