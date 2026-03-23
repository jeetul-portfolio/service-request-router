FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod ./

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o service-request-router .

FROM alpine:3.20

WORKDIR /app

COPY --from=builder /app/service-request-router /app/service-request-router
COPY --from=builder /app/config.json /app/config.json

ENV PORT=8080

EXPOSE 8080

CMD ["/app/service-request-router", "-config", "/app/config.json"]
