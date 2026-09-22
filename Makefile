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

.PHONY: lint
lint: ## проверить формат и статический анализ
	@test -z "$$(gofmt -l . | grep -v '^$$')" || (echo "не отформатированы:"; gofmt -l .; exit 1)
	go vet ./...

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
