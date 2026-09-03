#!/bin/sh
# Поднимает настоящий сервер для e2e: тот же бинарник, что и в проде,
# с вшитым веб-клиентом.
#
# Токен задан намеренно: без него проверка доступа выключена целиком,
# и тесты на вход и на изоляцию временных ссылок ничего бы не проверяли.
set -e
cd "$(dirname "$0")/../.."

rm -rf .e2e-data
mkdir -p .e2e-data

npm --prefix web run build >/dev/null
go build -o .e2e-data/svodd ./cmd/svodd

export SVOD_TOKEN=e2e-test-token-not-for-production
exec .e2e-data/svodd -addr :8123 -data .e2e-data
