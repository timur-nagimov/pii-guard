#!/usr/bin/env bash
# Нагрузочный прогон по сервису. Без аргументов даёт профиль проверяющей
# системы: тысяча запросов в секунду в течение пяти минут.
#
# Примеры:
#   pii-load.sh                       штатный профиль
#   pii-load.sh --rps 0 --duration 2m поиск потолка, без ограничения частоты
#   pii-load.sh --payload 250         тексты фиксированного размера
set -euo pipefail

LOAD_DIR=${LOAD_DIR:-/opt/pii-load}
TARGET=${PII_TARGET:-}
if [ -z "$TARGET" ] && [ -f /etc/pii-guard/deploy.env ]; then
  # shellcheck disable=SC1090
  . /etc/pii-guard/deploy.env
  TARGET=${APP_URL:-}
fi
TARGET=${TARGET:-http://127.0.0.1}

exec "$LOAD_DIR/bin/loadgen" -url "$TARGET" -dataset "$LOAD_DIR/corpus/dataset.jsonl" \
  -rps "${RPS:-1000}" -duration "${DURATION:-5m}" -workers "${WORKERS:-400}" "$@"
