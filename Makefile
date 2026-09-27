VAULT  ?= $(HOME)/obsidian/Vk
SERVER ?= http://localhost:8080

.PHONY: help app web build run daemon sync status up down logs rebuild clean \
        image provision deploy deploy-all restart

help:
	@echo "Свод — команды"
	@echo ""
	@echo "  make app       свод как приложение: сервер, демон и браузер локально"
	@echo "  make up        поднять сервер в Docker на :8080"
	@echo "  make daemon    запустить демона на маке (следит за VAULT)"
	@echo "  make sync      разовая заливка свода без слежения"
	@echo "  make status    что демон думает о состоянии"
	@echo ""
	@echo "  make web       собрать веб-клиент"
	@echo "  make build     собрать бинарники в ./bin"
	@echo "  make run       поднять сервер без Docker"
	@echo "  make logs      логи сервера"
	@echo "  make down      остановить сервер"
	@echo "  make rebuild   пересобрать образ с нуля"
	@echo ""
	@echo "  make provision подготовить VPS: обновления, файрвол, Docker"
	@echo "  make image     собрать образ и упаковать для отправки"
	@echo "  make deploy    выложить новую версию на VPS"
	@echo "  make deploy-all  подготовка и выкладка одной командой"
	@echo "  make restart IP=x.x.x.x  поднять свод после остановки ВМ"
	@echo ""
	@echo "  VAULT = $(VAULT)"

web:
	rm -rf internal/webui/dist/assets
	cd web && npm install --no-audit --no-fund && npm run build

build: web
	go build -trimpath -o bin/svodd    ./cmd/svodd
	go build -trimpath -o bin/svod     ./cmd/svod
	go build -trimpath -o bin/svod-mcp ./cmd/svod-mcp

# Свод как приложение: локальный сервер, демон к нему и браузер.
# Сборку делает сам скрипт, если бинарников ещё нет.
app:
	VAULT="$(VAULT)" sh scripts/app.sh

run: build
	./bin/svodd -addr :8080 -data ./data

up:
	docker compose up -d --build
	@echo "сервер на http://localhost:8080"

down:
	docker compose down

logs:
	docker compose logs -f svodd

rebuild:
	docker compose build --no-cache
	docker compose up -d

daemon:
	go run ./cmd/svod -vault "$(VAULT)" -server "$(SERVER)"

sync:
	go run ./cmd/svod -vault "$(VAULT)" -server "$(SERVER)" -once

status:
	go run ./cmd/svod -vault "$(VAULT)" -server "$(SERVER)" -status

clean:
	rm -rf bin data web/node_modules internal/webui/dist/assets internal/webui/dist/index.html

# ── Выкладка на VPS ───────────────────────────────────────────────────

IMAGE ?= svod:latest
# Архитектура сервера, а не своей машины: собранный на Apple Silicon
# arm64-образ на обычном VPS падает с exec format error.
PLATFORM ?= linux/amd64

# Образ собираем у себя и отправляем архивом: реестр не нужен,
# работает с первого дня.
image:
	docker build --platform $(PLATFORM) -t $(IMAGE) .
	mkdir -p deploy/.build
	docker save $(IMAGE) | gzip > deploy/.build/$(subst :,-,$(IMAGE)).tar.gz
	@ls -lh deploy/.build/$(subst :,-,$(IMAGE)).tar.gz

provision:
	cd deploy && ansible-playbook provision.yml

deploy: image
	cd deploy && ansible-playbook deploy.yml

deploy-all: image
	cd deploy && ansible-playbook site.yml

# После остановки ВМ меняется IP: make restart IP=<новый адрес>.
# Без IP берётся тот, что уже записан в inventory.ini.
restart:
ifdef IP
	sed -i '' -E 's/ansible_host=[0-9.]+/ansible_host=$(IP)/' deploy/inventory.ini
endif
	cd deploy && ansible-playbook restart.yml --ssh-common-args='-o StrictHostKeyChecking=accept-new'

check:
	cd deploy && ansible-playbook site.yml --syntax-check
