#!/bin/sh
# Поднимает настоящий сервер для e2e: тот же бинарник, что и в проде,
# с вшитым веб-клиентом. Тесты ходят на него, а не на дев-сервер vite.
set -e
cd "$(dirname "$0")/../.."

rm -rf .e2e-data
mkdir -p .e2e-data

npm --prefix web run build >/dev/null
go build -o .e2e-data/svodd ./cmd/svodd

exec .e2e-data/svodd -addr :8123 -data .e2e-data
