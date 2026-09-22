#!/usr/bin/env bash
# Готовит машину генератора нагрузки: собирает инструменты прогона и набор
# данных, чтобы нагрузочный прогон запускался одной командой.
set -euo pipefail

ENV_FILE=/etc/pii-guard/deploy.env
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
fi

LOAD_DIR=${LOAD_DIR:-/opt/pii-load}
SRC_DIR=$LOAD_DIR/src
SOURCE_URL=${SOURCE_URL:-}
REPO_URL=${REPO_URL:-}
REPO_REF=${REPO_REF:-main}
GO_VERSION=${GO_VERSION:-1.26.0}
CORPUS_SIZE=${CORPUS_SIZE:-8000}

log() { echo "[pii-load-deploy] $*"; }

fetch() {
  local url=$1 dest=$2 attempt
  for attempt in 1 2 3 4 5; do
    if curl -fsSL --connect-timeout 10 --max-time 900 -o "$dest" "$url"; then
      return 0
    fi
    log "попытка $attempt не удалась, повторяю: $url"
    sleep 5
  done
  return 1
}

install_go() {
  if command -v go >/dev/null 2>&1 && go version | grep -q "go${GO_VERSION} "; then
    return 0
  fi
  log "ставлю Go ${GO_VERSION}"
  fetch "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  rm -f /tmp/go.tar.gz
}

get_source() {
  if [ -n "$SOURCE_URL" ]; then
    rm -rf "$SRC_DIR"
    mkdir -p "$SRC_DIR"
    fetch "$SOURCE_URL" /tmp/src.tar.gz
    tar -C "$SRC_DIR" --strip-components=1 -xzf /tmp/src.tar.gz
    rm -f /tmp/src.tar.gz
    return 0
  fi
  if [ -n "$REPO_URL" ]; then
    rm -rf "$SRC_DIR"
    git clone --depth 1 --branch "$REPO_REF" "$REPO_URL" "$SRC_DIR"
    return 0
  fi
  return 1
}

mkdir -p "$LOAD_DIR/bin" "$LOAD_DIR/corpus"
if get_source; then
  install_go
  for tool in loadgen sonar gen; do
    log "собираю ${tool}"
    (cd "$SRC_DIR" && CGO_ENABLED=0 /usr/local/bin/go build -trimpath -o "$LOAD_DIR/bin/$tool" "./cmd/$tool")
  done
  if [ ! -s "$LOAD_DIR/corpus/dataset.jsonl" ]; then
    log "готовлю набор данных на ${CORPUS_SIZE} элементов"
    "$LOAD_DIR/bin/gen" -out "$LOAD_DIR/corpus/dataset.jsonl" -n "$CORPUS_SIZE" -seed 42
  fi
  chown -R ubuntu:ubuntu "$LOAD_DIR"
else
  log "источник не задан, инструменты прогона не собраны"
  log "задайте app_source_url или app_repo_url и выполните скрипт повторно"
fi
