# syntax=docker/dockerfile:1

# Production image. It is built in GitHub Actions and published to GHCR: the
# server never compiles, it only downloads.

FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22 AS runner
# ca-certificates: TLS towards WhatsApp and the tenants' webhooks.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=builder /out/api /app/api
USER app
ENV HTTP_PORT=8080 APP_ENV=production
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/app/api", "healthcheck"]
ENTRYPOINT ["/app/api"]
CMD ["serve"]
