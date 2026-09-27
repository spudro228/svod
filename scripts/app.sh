#!/bin/sh
# Свод как приложение на своей машине: локальный сервер, демон к нему
# и браузер. Интернет и ВМ не нужны.
#
#   браузер ⇄ svodd (127.0.0.1) ⇄ демон ⇄ папка свода ⇄ боевой демон ⇄ ВМ
#
# Правка в браузере через секунду лежит на диске, а дальше её, как любую
# правку с диска, развозит боевой демон, если он запущен. Этот скрипт
# боевого демона не трогает: у локального своё состояние, поэтому они
# не путают позиции в логах двух разных серверов.
#
# Сервер слушает только 127.0.0.1 и потому работает без токена.
set -eu

cd "$(dirname "$0")/.."

VAULT=${VAULT:-$HOME/obsidian/Vk}
PORT=${PORT:-8090}
HOME_DIR=${SVOD_APP_DIR:-"$HOME/Library/Application Support/svod-app"}
URL="http://127.0.0.1:$PORT"

if [ ! -d "$VAULT" ]; then
	echo "свод не найден: $VAULT" >&2
	exit 1
fi

# Порт занят — скорее всего, приложение уже открыто. Второй сервер
# на тех же данных не поднимаем, просто показываем открытое.
if curl -fsS -m 2 "$URL/healthz" >/dev/null 2>&1; then
	echo "свод уже работает: $URL"
	open "$URL"
	exit 0
fi

if [ ! -x bin/svodd ] || [ ! -x bin/svod ]; then
	make build
fi

mkdir -p "$HOME_DIR/data"

pids=""
stop() {
	# shellcheck disable=SC2086
	[ -n "$pids" ] && kill $pids 2>/dev/null || true
	wait 2>/dev/null || true
	echo "свод остановлен"
}
trap stop EXIT
trap 'exit 0' INT TERM

./bin/svodd -addr "127.0.0.1:$PORT" -data "$HOME_DIR/data" \
	>>"$HOME_DIR/svodd.log" 2>&1 &
pids="$!"

i=0
until curl -fsS -m 1 "$URL/healthz" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 50 ]; then
		echo "сервер не поднялся, смотри $HOME_DIR/svodd.log" >&2
		exit 1
	fi
	sleep 0.1
done

./bin/svod -vault "$VAULT" -server "$URL" -state "$HOME_DIR/state.db" \
	-device "$(hostname -s) · приложение" >>"$HOME_DIR/svod.log" 2>&1 &
pids="$pids $!"

echo "свод: $URL"
echo "папка: $VAULT"
echo "журналы: $HOME_DIR"
echo "Ctrl-C — закрыть"
open "$URL"

wait
