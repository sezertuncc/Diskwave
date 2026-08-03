FROM golang:1.23-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git protobuf

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -ldflags="-s -w" -o /diskside-server ./cmd/server

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /diskside-server /app/diskside-server

RUN mkdir -p /data/blocks /tmp/diskside-staging

EXPOSE 7878/udp

CMD ["/app/diskside-server"]