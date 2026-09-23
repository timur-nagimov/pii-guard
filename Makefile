.DEFAULT_GOAL := help
SHELL := /bin/bash
BIN := bin/pii-guard
CONFIG ?= configs/config.yaml
CORPUS ?= corpus/dataset.jsonl
URL ?= http://127.0.0.1:8080

.PHONY: help
help: ## показать список целей
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## собрать сервис
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/pii-guard

.PHONY: run
run: ## запустить сервис с настройками по умолчанию
	go run ./cmd/pii-guard -config $(CONFIG)

.PHONY: test
test: ## прогнать тесты с детектором гонок
	go test ./... -race -count=1

.PHONY: cover
cover: ## прогнать тесты и показать покрытие
	go test ./... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

.PHONY: sonar-prep
sonar-prep: ## подготовить отчёты для сканера SonarQube
	go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
	go test ./... -count=1 -json > test-report.json
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./... --output.checkstyle.path=golangci-report.xml || true; \
	else \
		echo "golangci-lint не установлен, отчёт линтера не собран"; \
	fi
	go vet ./... 2> govet-report.out || true

.PHONY: lint
lint: ## проверить формат и статический анализ
	@test -z "$$(gofmt -l . | grep -v '^$$')" || (echo "не отформатированы:"; gofmt -l .; exit 1)
	go vet ./...
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "установите: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; \
	fi
	# internal/pii подключается после слияния ветки sonar-lint-pii
	golangci-lint run ./cmd/... ./internal/api/... ./internal/config/... ./internal/engine/... ./internal/logging/... ./internal/mask/... ./internal/metrics/... ./internal/store/...

.PHONY: check-config
check-config: ## проверить файл настроек
	go run ./cmd/pii-guard -check-config -config $(CONFIG)

.PHONY: corpus
corpus: ## сгенерировать размеченный набор данных
	mkdir -p corpus
	go run ./cmd/gen -out $(CORPUS) -n 8000 -seed 42

.PHONY: quality
quality: corpus ## измерить качество на своём наборе данных
	go run ./cmd/sonar -url $(URL) -dataset $(CORPUS) -rps 50 -duration 30s -out QUALITY

.PHONY: bench
bench: ## полный нагрузочный прогон, как у проверяющей системы
	go run ./cmd/sonar -url $(URL) -dataset $(CORPUS) -rps 1000 -duration 5m -out BENCHMARKS

.PHONY: verify
verify: ## короткая проверка: контракт, маска, обратное преобразование
	@bash scripts/verify.sh $(URL)

.PHONY: logs
logs: ## показать журнал сервиса
	@if docker compose ps --status running app >/dev/null 2>&1 && \
	    [ -n "$$(docker compose ps -q app 2>/dev/null)" ]; then \
		docker compose logs -f app; \
	elif systemctl is-active --quiet pii-guard 2>/dev/null; then \
		journalctl -u pii-guard -f --no-pager; \
	else \
		echo "Сервис не найден ни в docker compose, ни в systemd."; \
		echo "Если запущен через make run, журнал идёт в тот же терминал."; \
		echo "Журнал пишется в стандартный вывод в формате JSON, читать удобно так:"; \
		echo "  make run 2>&1 | python3 -m json.tool --json-lines"; \
		exit 1; \
	fi

.PHONY: gate
gate: ## ворота качества: семнадцать проверок, один код возврата
	@bash scripts/gate.sh

.PHONY: gate-quick
gate-quick: ## ворота без набора данных и без схем, для быстрой проверки
	@bash scripts/gate.sh --quick

.PHONY: gate-selfcheck
gate-selfcheck: ## проверить, что сами ворота ловят то, что обещают
	@bash scripts/gate-selfcheck.sh

.PHONY: jury-check
jury-check: ## прогнать все проверочные сценарии для жюри
	@bash scripts/jury.sh $(URL) all

.PHONY: docker
docker: ## собрать образ под linux/amd64
	docker build --platform linux/amd64 -t pii-guard:latest .

.PHONY: up
up: ## поднять сервис и наблюдение
	docker compose up -d --build

.PHONY: down
down: ## остановить сервис
	docker compose down

.PHONY: dist
dist: ## собрать архив с исходным кодом для загрузки
	mkdir -p dist
	git archive --format=zip --output dist/pii-guard.zip HEAD
	@echo "размер архива: $$(du -h dist/pii-guard.zip | cut -f1)"

.PHONY: dist-check
dist-check: dist ## проверить, что в архиве нет лишнего
	@bash scripts/dist-check.sh dist/pii-guard.zip

# Нагрузочное тестирование. Ключи скрипта передаются через LOAD_ARGS,
# например: make load-ceiling LOAD_ARGS="--remote --url http://10.129.0.22"
LOAD_ARGS ?=

.PHONY: load-smoke
load-smoke: ## нагрузка: короткая проверка за тридцать секунд
	@bash scripts/loadtest.sh smoke --url $(URL) $(LOAD_ARGS)

.PHONY: load-sla
load-sla: ## нагрузка: прогон по правилам проверяющей системы, 1000 в секунду пять минут
	@bash scripts/loadtest.sh sla --url $(URL) $(LOAD_ARGS)

.PHONY: load-ceiling
load-ceiling: ## нагрузка: поиск потолка ступенями на текстах 250 Б, 2 КБ и 8 КБ
	@bash scripts/loadtest.sh ceiling --url $(URL) $(LOAD_ARGS)

.PHONY: load-soak
load-soak: ## нагрузка: длительный прогон на утечки с показателями до и после
	@bash scripts/loadtest.sh soak --url $(URL) $(LOAD_ARGS)

# Цель названа без цифр: список целей в help собирается по буквам и дефисам.
.PHONY: load-opinion
load-opinion: ## нагрузка: второе мнение, сценарий k6 на машине генератора
	@bash scripts/loadtest.sh k6 --remote --url $(URL) $(LOAD_ARGS)
