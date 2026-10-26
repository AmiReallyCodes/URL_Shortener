ARG GO_VERSION=1.25

# Stage 1: build
FROM golang:alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o main ./api

# Stage 2: minimal runtime image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && adduser -S -D -H -h /app appuser
WORKDIR /app
COPY --from=builder --chown=appuser:appuser /build/main .
USER appuser
EXPOSE 3001
CMD ["./main"]