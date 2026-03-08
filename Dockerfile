FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S relay && adduser -S -G relay relay

WORKDIR /app

COPY --from=builder /out/relay /usr/local/bin/relay

RUN mkdir -p /var/lib/aethos && chown -R relay:relay /var/lib/aethos

USER relay

EXPOSE 8080 8081

VOLUME ["/var/lib/aethos"]

ENTRYPOINT ["/usr/local/bin/relay"]
CMD ["-ws-addr=:8080", "-http-addr=:8081", "-store-path=/var/lib/aethos/relay.db"]
