# ==========================================
# Fase 1: Builder
# ==========================================
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o proxyma .

# ==========================================
# Fase 2: Runner
# ==========================================
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/proxyma .

ENV PROXYMA_STORAGE=/app/data
RUN mkdir -p /app/data

EXPOSE 8080

ENTRYPOINT ["./proxyma"]
CMD ["run", "--debug", "true"]
