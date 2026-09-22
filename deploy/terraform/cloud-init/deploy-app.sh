#!/usr/bin/env bash
# Разворачивает сервис маскирования персональных данных на чистой машине.
#
# Скрипт идемпотентный: его можно запускать повторно, он доводит машину до
# рабочего состояния и не ломает уже работающий сервис. Все настройки приходят
# из /etc/pii-guard/deploy.env, который пишет облачная инициализация.
set -euo pipefail

ENV_FILE=/etc/pii-guard/deploy.env
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
fi

APP_DIR=${APP_DIR:-/opt/pii-guard}
SRC_DIR=$APP_DIR/src
BIN=$APP_DIR/pii-guard
ENV_OUT=$APP_DIR/pii.env
BINARY_URL=${BINARY_URL:-}
SOURCE_URL=${SOURCE_URL:-}
REPO_URL=${REPO_URL:-}
REPO_REF=${REPO_REF:-main}
GO_VERSION=${GO_VERSION:-1.26.0}
STORE_KEY=${STORE_KEY:-}
REDIS_ADDR=${REDIS_ADDR:-}
REDIS_PASSWORD=${REDIS_PASSWORD:-}
SERVICE_USER=${SERVICE_USER:-ubuntu}
LOGGING_DIR=$APP_DIR/deploy/logging
LOGGING_PACK=""

log() { echo "[pii-guard-deploy] $*"; }

# fetch скачивает файл с повторами: сеть машины поднимается не мгновенно.
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

# unpack_source кладёт исходный код в каталог назначения. По умолчанию это
# $SRC_DIR, но каталог задаётся вторым доводом: тем же кодом скачивается пакет
# настроек журнала, когда сервис разворачивают из готового исполняемого файла.
unpack_source() {
  local url=$1 dest=${2:-$SRC_DIR}
  rm -rf "$dest"
  mkdir -p "$dest"
  case "$url" in
    *.zip)
      fetch "$url" /tmp/src.zip
      rm -rf /tmp/src
      unzip -q /tmp/src.zip -d /tmp/src
      # Архив может содержать один верхний каталог, а может не содержать.
      if [ "$(find /tmp/src -maxdepth 1 -mindepth 1 | wc -l)" = "1" ] && [ -d "$(find /tmp/src -maxdepth 1 -mindepth 1)" ]; then
        mv "$(find /tmp/src -maxdepth 1 -mindepth 1)"/* "$dest"/
      else
        mv /tmp/src/* "$dest"/
      fi
      rm -rf /tmp/src /tmp/src.zip
      ;;
    *)
      fetch "$url" /tmp/src.tar.gz
      tar -C "$dest" --strip-components=1 -xzf /tmp/src.tar.gz
      rm -f /tmp/src.tar.gz
      ;;
  esac
}

# obtain_binary кладёт исполняемый файл в $BIN. Источники перебираются по
# убыванию скорости: готовый файл, архив с исходным кодом, репозиторий.
obtain_binary() {
  if [ -n "$BINARY_URL" ]; then
    log "скачиваю готовый исполняемый файл"
    fetch "$BINARY_URL" /tmp/pii-guard
    install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" /tmp/pii-guard "$BIN"
    rm -f /tmp/pii-guard
    return 0
  fi
  if [ -n "$SOURCE_URL" ]; then
    log "собираю из архива с исходным кодом"
    unpack_source "$SOURCE_URL"
  elif [ -n "$REPO_URL" ]; then
    log "собираю из репозитория ${REPO_URL} (${REPO_REF})"
    rm -rf "$SRC_DIR"
    git clone --depth 1 --branch "$REPO_REF" "$REPO_URL" "$SRC_DIR"
  else
    log "источник сервиса не задан: задайте app_binary_url, app_source_url или app_repo_url"
    return 1
  fi
  install_go
  (cd "$SRC_DIR" && CGO_ENABLED=0 /usr/local/bin/go build -trimpath -o /tmp/pii-guard ./cmd/pii-guard)
  install -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" /tmp/pii-guard "$BIN"
  rm -f /tmp/pii-guard
}

write_env() {
  if [ -z "$STORE_KEY" ]; then
    if [ -f "$ENV_OUT" ] && grep -q '^PII_STORE_KEY=..*$' "$ENV_OUT"; then
      # Ключ уже сгенерирован прошлым запуском, менять его нельзя: иначе
      # перестанут расшифровываться записи, созданные до перезапуска.
      STORE_KEY=$(grep '^PII_STORE_KEY=' "$ENV_OUT" | cut -d= -f2-)
    else
      STORE_KEY=$(openssl rand -base64 32)
      log "ключ шифрования хранилища сгенерирован на машине"
    fi
  fi
  umask 077
  {
    echo "PII_STORE_KEY=${STORE_KEY}"
    echo "PII_REDIS_ADDR=${REDIS_ADDR}"
    echo "PII_REDIS_PASSWORD=${REDIS_PASSWORD}"
  } > "$ENV_OUT"
  chown "$SERVICE_USER":"$SERVICE_USER" "$ENV_OUT"
  chmod 0600 "$ENV_OUT"
}

# --- Сбор журнала ---
#
# Пакет настроек лежит в deploy/logging: пределы journald, дополнение к службе
# с уровнем журнала и путями аудита, почасовая архивация аудита. Ставит его
# install.sh из самого пакета, здесь только поиск пакета на машине.
#
# Почему это делает скрипт, а не сама облачная инициализация. Шаблон
# cloud-init получает готовый набор переменных от compute.tf и scaling.tf, и
# положить файлы пакета через write_files нельзя, не добавив в оба вызова
# templatefile ещё одну переменную, то есть не правя чужие файлы. Скрипт же
# едет на машину целиком и выполняется и на машине сервиса, и на копиях в
# группе, поэтому установка живёт здесь и в одном экземпляре.
#
# Слой Loki остаётся выключенным: его файлы копируются вместе с пакетом, но
# ничто их не запускает, как и задумано в docs/LOGGING.md.

# find_logging_pack ищет пакет по убыванию свежести и кладёт путь в
# LOGGING_PACK: исходники этого разворачивания, уже лежащий на машине пакет,
# исходники по ссылке. Последний случай нужен при разворачивании из готового
# исполняемого файла: исходников на машине нет, а несколько файлов настроек
# нужны.
find_logging_pack() {
  local dir
  for dir in "$SRC_DIR/deploy/logging" "$LOGGING_DIR"; do
    if [ -f "$dir/install.sh" ]; then
      LOGGING_PACK=$dir
      return 0
    fi
  done

  local tmp=/tmp/pii-guard-logging-src
  if [ -n "$SOURCE_URL" ]; then
    log "исходников на машине нет, качаю пакет журнала из архива"
    unpack_source "$SOURCE_URL" "$tmp" || return 1
  elif [ -n "$REPO_URL" ]; then
    log "исходников на машине нет, качаю пакет журнала из репозитория"
    rm -rf "$tmp"
    git clone --depth 1 --branch "$REPO_REF" "$REPO_URL" "$tmp" || return 1
  else
    return 1
  fi

  [ -f "$tmp/deploy/logging/install.sh" ] || return 1
  LOGGING_PACK="$tmp/deploy/logging"
}

install_logging() {
  if ! find_logging_pack; then
    log "пакет сбора журнала не найден: ни в $SRC_DIR/deploy/logging, ни в $LOGGING_DIR, ни по ссылке на исходники"
    log "journald останется с умолчаниями и на нагрузке молча отбросит всё сверх 10000 сообщений за 30 секунд"
    return 0
  fi

  if [ "$LOGGING_PACK" != "$LOGGING_DIR" ]; then
    mkdir -p "$LOGGING_DIR"
    cp -R "$LOGGING_PACK/." "$LOGGING_DIR/"
  fi
  chmod 0755 "$LOGGING_DIR"/*.sh 2>/dev/null || true

  log "ставлю сбор журнала: пределы journald, дополнение к службе, архивация аудита"
  if bash "$LOGGING_DIR/install.sh"; then
    log "сбор журнала настроен"
  else
    log "установка сбора журнала не удалась, смотрите journalctl -u systemd-journald"
  fi
}

start_service() {
  systemctl daemon-reload
  systemctl enable pii-guard.service
  systemctl restart pii-guard.service
}

wait_ready() {
  local attempt
  for attempt in $(seq 1 30); do
    if curl -fsS --max-time 2 "http://127.0.0.1:${HTTP_PORT:-80}/readyz" >/dev/null 2>&1; then
      log "сервис отвечает на /readyz"
      return 0
    fi
    sleep 2
  done
  log "сервис не ответил на /readyz за минуту, смотрите journalctl -u pii-guard"
  return 1
}

mkdir -p "$APP_DIR" "$APP_DIR/configs"
chown -R "$SERVICE_USER":"$SERVICE_USER" "$APP_DIR"
write_env
if obtain_binary; then
  # Сбор журнала ставится до запуска службы: дополнение к ней задаёт уровень
  # журнала, каталог аудита и снимает ограничитель частоты journald, и всё это
  # должно действовать уже с первого запуска, а не со следующего перезапуска.
  # Вызов через ||: сбор журнала это не причина не поднять сервис. Что именно
  # не получилось, скрипт уже сказал в журнал облачной инициализации.
  install_logging || log "сбор журнала не настроен, разворачивание продолжается"
  start_service
  wait_ready || true
else
  log "исполняемый файл не получен, служба остаётся остановленной"
  log "положите файл в ${BIN} и выполните: systemctl start pii-guard"
fi
