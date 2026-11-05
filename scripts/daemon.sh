#!/bin/sh
# Боевой демон на маке: проверка и перезапуск.
#
#   scripts/daemon.sh check                 что с демоном и сервером
#   scripts/daemon.sh restart               перезапустить как есть
#   scripts/daemon.sh restart IP            сменить адрес на svod.IP.sslip.io
#   scripts/daemon.sh restart https://…     сменить адрес на произвольный
#   scripts/daemon.sh logs [N] [errors]     журнал в реальном времени
#
# Демон живёт в launchd, а все его настройки — аргументы в plist.
# Отсюда и берём адрес, токен и папку, чтобы не держать их в двух местах.
# Токен никуда не печатается.
set -eu

LABEL=com.svod.daemon
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$HOME/Library/Logs/svod/daemon.log"
ERR_LOG="$HOME/Library/Logs/svod/daemon.err.log"
DOMAIN="gui/$(id -u)"
PB=/usr/libexec/PlistBuddy

if [ ! -f "$PLIST" ]; then
	echo "демон не установлен: нет $PLIST" >&2
	exit 1
fi

# Индекс аргумента в ProgramArguments; пусто, если его нет.
arg_index() {
	i=0
	while v=$($PB -c "Print :ProgramArguments:$i" "$PLIST" 2>/dev/null); do
		if [ "$v" = "$1" ]; then
			echo "$i"
			return
		fi
		i=$((i + 1))
	done
}

# Значение после флага: -server https://… → https://…
arg_value() {
	i=$(arg_index "$1")
	[ -n "$i" ] || return 0
	$PB -c "Print :ProgramArguments:$((i + 1))" "$PLIST"
}

bin=$($PB -c "Print :ProgramArguments:0" "$PLIST")
vault=$(arg_value -vault)
server=$(arg_value -server)
token=$(arg_value -token)

check() {
	echo "── launchd"
	if info=$(launchctl print "$DOMAIN/$LABEL" 2>/dev/null); then
		echo "$info" | awk '
			/^\tstate = /           { print "  состояние:  " $3 }
			/^\tpid = /             { print "  pid:        " $3 }
			/^\truns = /            { print "  запусков:   " $3 }
			/^\tlast exit code = /  { sub(/^\tlast exit code = /, ""); print "  выход:      " $0 }'
	else
		echo "  не загружен в launchd — make daemon-restart загрузит"
	fi

	echo "── демон"
	echo "  бинарник:   $bin ($("$bin" -version 2>/dev/null || echo 'не запускается'))"
	echo "  папка:      $vault"
	echo "  сервер:     $server"

	echo "── сервер"
	if health=$(curl -fsS -m 5 "$server/healthz" 2>&1); then
		echo "  отвечает:   $health"
	else
		echo "  НЕ ОТВЕЧАЕТ: $health"
		echo "  если сменился IP ВМ: make restart IP=<ip> && make daemon-restart IP=<ip>"
	fi

	echo "── синхронизация"
	"$bin" -vault "$vault" -server "$server" -token "$token" -status 2>&1 | sed 's/^/  /'

	echo "── последние ошибки"
	errs=$(grep -hE 'level=(WARN|ERROR)' "$LOG" "$ERR_LOG" 2>/dev/null | tail -5 || true)
	if [ -n "$errs" ]; then
		echo "$errs" | sed 's/^/  /'
	else
		echo "  нет"
	fi
}

restart() {
	new=${1:-}
	case "$new" in
	"") ;;
	http://* | https://*) ;;
	*) new="https://svod.$new.sslip.io" ;;
	esac

	if [ -n "$new" ] && [ "$new" != "$server" ]; then
		i=$(arg_index -server)
		if [ -z "$i" ]; then
			echo "в plist нет аргумента -server" >&2
			exit 1
		fi
		cp "$PLIST" "$PLIST.bak"
		$PB -c "Set :ProgramArguments:$((i + 1)) $new" "$PLIST"
		echo "сервер: $server → $new (прежний plist в $PLIST.bak)"
		server=$new
	fi

	# launchd читает plist только при загрузке, поэтому выгружаем
	# и загружаем заново: kickstart перезапустил бы со старыми аргументами.
	launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
	# bootout возвращается раньше, чем служба действительно ушла.
	i=0
	while launchctl print "$DOMAIN/$LABEL" >/dev/null 2>&1 && [ "$i" -lt 50 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	launchctl bootstrap "$DOMAIN" "$PLIST"
	echo "демон перезапущен"

	# Первичная синхронизация занимает пару секунд — даём ей пройти
	# и показываем, чем кончилось.
	sleep 3
	echo
	check
}

# Журнал в реальном времени. Оба файла вместе: launchd пишет stdout
# и stderr раздельно, а читать их удобно одной лентой.
logs() {
	n=${1:-50}
	only_errors=${2:-}
	# -F, а не -f: переживает пересоздание файла при ротации.
	if [ -n "$only_errors" ]; then
		pattern='level=(WARN|ERROR)|panic|fatal'
		# Сначала последние N ошибок из истории, потом только новые:
		# tail -f за пайпом на macOS не следит, поэтому в два шага.
		grep -hE "$pattern" "$LOG" "$ERR_LOG" 2>/dev/null | tail -n "$n" || true
		tail -q -n 0 -F "$LOG" "$ERR_LOG" 2>/dev/null | grep --line-buffered -E "$pattern"
	else
		tail -q -n "$n" -F "$LOG" "$ERR_LOG" 2>/dev/null
	fi
}

case "${1:-check}" in
check) check ;;
restart) restart "${2:-}" ;;
logs) logs "${2:-}" "${3:-}" ;;
*)
	echo "использование: $0 check | restart [IP | https://адрес] | logs [строк] [errors]" >&2
	exit 2
	;;
esac
