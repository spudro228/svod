# ── Фронт ─────────────────────────────────────────────────────────────
# BUILDPLATFORM: собираем на своей архитектуре, результат от неё не зависит.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
# npm 10 в этом образе ставит install-скрипты сам, esbuild получит бинарник
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ── Сервер ────────────────────────────────────────────────────────────
# Тоже на своей архитектуре: Go кросс-компилирует сам, и это в разы
# быстрее, чем гонять компилятор под QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Собранный фронт кладём туда, откуда его забирает go:embed
COPY --from=web /app/internal/webui/dist ./internal/webui/dist
# modernc.org/sqlite — чистый Go, cgo не нужен, поэтому кросс-сборка
# сводится к одной переменной окружения.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/svodd ./cmd/svodd

# ── Рантайм ───────────────────────────────────────────────────────────
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
 && adduser -D -u 10001 svod
COPY --from=build /out/svodd /usr/local/bin/svodd
USER svod
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["svodd", "-addr", ":8080", "-data", "/data"]
