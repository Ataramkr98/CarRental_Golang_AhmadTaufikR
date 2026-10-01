# ---- Build stage ----
FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags='-s -w' -o /server .

# ---- Runtime stage ----
FROM alpine:3.20
# wget (busybox) backs the compose healthcheck; ca-certificates is needed for
# outbound TLS to Xendit and the managed Postgres instance.
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=builder /server .

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --spider -q http://localhost:8080/api/health || exit 1

CMD ["./server"]
