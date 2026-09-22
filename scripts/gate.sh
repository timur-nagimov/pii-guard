#!/usr/bin/env bash
# Ворота качества: одна команда, один код возврата.
#
# Ноль означает, что правку можно вливать. Любое другое число означает, что
# нельзя, и в выводе написано почему.
#
# Скрипт нужен там, где правку принимает не человек: ночной прогон, проверка
# перед слиянием, проверка перед сдачей. Смысл ворот в том, что они проверяют
# не «собирается ли код», а «не стало ли хуже». Поэтому половина проверок
# сравнивает результат с базовой линией, а не с нулём.
#
# ВАЖНО. Файл базовой линии scripts/baseline.json меняет только человек.
# Если его может менять тот же, кто правит код, ворота теряют смысл: любую
# просадку можно объявить новой нормой. Проверка этого правила стоит ниже.
#
# Использование:
#   bash scripts/gate.sh              полный прогон
#   bash scripts/gate.sh --quick      без нагрузки и без набора данных
#   bash scripts/gate.sh --list       показать состав проверок и выйти

set -o pipefail

cd "$(dirname "$0")/.." || exit 2
ROOT="$PWD"
BASELINE="$ROOT/scripts/baseline.json"
URL="${GATE_URL:-http://127.0.0.1:18081}"
QUICK=0
FAILED=0
PASSED=0
SKIPPED=0
REPORT=()

for arg in "$@"; do
  case "$arg" in
    --quick) QUICK=1 ;;
    --init)
      # Снимает текущее качество и записывает его как базовую линию.
      # Делается один раз человеком и потом только осознанно.
      CORPUS="${GATE_CORPUS:-corpus/dataset.jsonl}"
      if [ ! -f "$CORPUS" ]; then
        mkdir -p "$(dirname "$CORPUS")"
        echo "готовлю набор данных..."
        go run ./cmd/gen -out "$CORPUS" -n 8000 -seed 42 >/dev/null 2>&1
      fi
      echo "снимаю качество..."
      go run ./cmd/score -dataset "$CORPUS" >/tmp/gate-init.txt 2>&1 || {
        echo "замер не отработал, подробности /tmp/gate-init.txt"; exit 2; }
      python3 "$ROOT/scripts/gate-quality.py" --init /tmp/gate-init.txt > "$BASELINE" || exit 2
      echo "базовая линия записана: $BASELINE"
      echo "Проверьте её глазами и внесите в git отдельным коммитом."
      exit 0
      ;;
    --list)
      echo "Состав ворот:"
      echo "   1  формат исходников"
      echo "   2  статический анализ"
      echo "   3  сборка"
      echo "   4  тесты с детектором гонок"
      echo "   5  базовая линия не тронута"
      echo "   6  качество по типам не просело"
      echo "   7  отрицательные категории строго ноль"
      echo "   8  контракт: маска и обратное преобразование"
      echo "   9  сценарии жюри"
      echo "  10  персональные данные не в журнале и не в показателях"
      echo "  11  в архиве нет секретов и скомпилированных файлов"
      echo "  12  ссылки в документах целы"
      echo "  13  схемы Mermaid разбираются"
      echo "  14  запрещённые зависимости не добавлены"
      exit 0
      ;;
  esac
done

say() { printf '\n\033[1m── %s\033[0m\n' "$1"; }
ok()   { PASSED=$((PASSED+1)); REPORT+=("  ok    $1"); printf '  \033[32mok\033[0m    %s\n' "$1"; }
bad()  { FAILED=$((FAILED+1)); REPORT+=("  ПЛОХО $1"); printf '  \033[31mПЛОХО\033[0m %s\n' "$1"; }
skip() { SKIPPED=$((SKIPPED+1)); REPORT+=("  мимо  $1"); printf '  \033[33mмимо\033[0m  %s\n' "$1"; }

say "1-4. Код"

if [ -z "$(gofmt -l . 2>/dev/null | grep -v '^$')" ]; then
  ok "формат исходников"
else
  bad "формат исходников: $(gofmt -l . | tr '\n' ' ')"
fi

if go vet ./... >/tmp/gate-vet.log 2>&1; then
  ok "статический анализ"
else
  bad "статический анализ, подробности /tmp/gate-vet.log"
fi

if go build ./... >/tmp/gate-build.log 2>&1; then
  ok "сборка"
else
  bad "сборка, подробности /tmp/gate-build.log"
fi

if go test ./... -race -count=1 >/tmp/gate-test.log 2>&1; then
  ok "тесты с детектором гонок"
else
  bad "тесты, подробности /tmp/gate-test.log"
fi

say "5. Базовая линия"

# Ворота, которые можно подвинуть, это не ворота. Базовая линия принадлежит
# человеку: если она изменена в том же наборе правок, что и код, это повод
# остановиться и посмотреть глазами.
if [ ! -f "$BASELINE" ]; then
  bad "файла базовой линии нет, создайте его: bash scripts/gate.sh --init"
elif git rev-parse --git-dir >/dev/null 2>&1; then
  if git diff --quiet HEAD -- "$BASELINE" 2>/dev/null; then
    ok "базовая линия не тронута"
  else
    bad "базовая линия изменена вместе с кодом, так нельзя"
  fi
else
  skip "базовая линия: вне git, проверить нечем"
fi

say "6-7. Качество"

if [ "$QUICK" = "1" ]; then
  skip "качество по типам: быстрый режим"
  skip "отрицательные категории: быстрый режим"
elif [ ! -f "$BASELINE" ]; then
  skip "качество по типам: нет базовой линии"
else
  CORPUS="${GATE_CORPUS:-corpus/dataset.jsonl}"
  if [ ! -f "$CORPUS" ]; then
    mkdir -p "$(dirname "$CORPUS")"
    go run ./cmd/gen -out "$CORPUS" -n 8000 -seed 42 >/dev/null 2>&1
  fi

  if go run ./cmd/score -dataset "$CORPUS" >/tmp/gate-score.txt 2>&1; then
    # Оба правила проверяет измеритель: положительные срезы с допуском,
    # отрицательные строго с нулём. Держать их в одном месте важнее, чем
    # разделять ради красивого вывода.
    if python3 "$ROOT/scripts/gate-quality.py" "$BASELINE" /tmp/gate-score.txt >/tmp/gate-quality.txt 2>&1; then
      ok "качество по типам не просело"
      ok "отрицательные срезы строго ноль"
      grep -q "Стало лучше" /tmp/gate-quality.txt && sed -n '/Стало лучше/,/^$/p' /tmp/gate-quality.txt | sed 's/^/        /'
    else
      if grep -q "ложные срабатывания" /tmp/gate-quality.txt; then
        ok "качество по типам не просело"
        bad "отрицательные срезы: есть ложные срабатывания"
      else
        bad "качество просело"
        ok "отрицательные срезы строго ноль"
      fi
      sed 's/^/        /' /tmp/gate-quality.txt
    fi
  else
    bad "замер качества не отработал, подробности /tmp/gate-score.txt"
  fi
fi

say "8-10. Живой сервис"

SERVICE_PID=""
if curl -s -m 3 -o /dev/null "$URL/healthz" 2>/dev/null; then
  STARTED_HERE=0
else
  go build -o /tmp/gate-pii-guard ./cmd/pii-guard 2>/dev/null
  PII_STORE_KEY="$(head -c 32 /dev/urandom | base64)" \
    /tmp/gate-pii-guard -config configs/config.yaml >/tmp/gate-service.log 2>&1 &
  SERVICE_PID=$!
  STARTED_HERE=1
  for _ in $(seq 1 20); do
    curl -s -m 2 -o /dev/null "$URL/healthz" 2>/dev/null && break
    sleep 0.5
  done
fi

cleanup() {
  if [ -n "$SERVICE_PID" ]; then kill "$SERVICE_PID" 2>/dev/null; fi
}
trap cleanup EXIT

if curl -s -m 3 -o /dev/null "$URL/healthz" 2>/dev/null; then
  if bash scripts/verify.sh "$URL" >/tmp/gate-verify.log 2>&1; then
    ok "контракт: маска и обратное преобразование"
  else
    bad "контракт, подробности /tmp/gate-verify.log"
  fi

  if bash scripts/jury.sh "$URL" all >/tmp/gate-jury.log 2>&1; then
    ok "сценарии жюри"
  else
    bad "сценарии жюри, подробности /tmp/gate-jury.log"
  fi

  # Самая важная проверка всего файла. Значение персональных данных,
  # попавшее в журнал или в показатели, обесценивает решение целиком.
  if bash scripts/gate-noleak.sh "$URL" /tmp/gate-service.log >/tmp/gate-leak.log 2>&1; then
    ok "персональные данные не в журнале и не в показателях"
  else
    bad "УТЕЧКА в журнал или показатели, подробности /tmp/gate-leak.log"
  fi
else
  bad "сервис не поднялся, подробности /tmp/gate-service.log"
fi

say "11-14. Поставка"

if bash scripts/dist-check.sh >/tmp/gate-dist.log 2>&1; then
  ok "в архиве нет секретов и скомпилированных файлов"
else
  bad "в архив попало лишнее, подробности /tmp/gate-dist.log"
fi

if python3 "$ROOT/scripts/gate-links.py" >/tmp/gate-links.log 2>&1; then
  ok "ссылки в документах целы"
else
  bad "битые ссылки:"
  sed 's/^/        /' /tmp/gate-links.log
fi

if [ "$QUICK" = "1" ]; then
  skip "схемы Mermaid: быстрый режим"
elif command -v mmdc >/dev/null 2>&1 || [ -x /tmp/mermaid-check/node_modules/.bin/mmdc ]; then
  if bash scripts/gate-mermaid.sh >/tmp/gate-mermaid.log 2>&1; then
    ok "схемы Mermaid разбираются"
  else
    bad "сломанные схемы, подробности /tmp/gate-mermaid.log"
  fi
else
  skip "схемы Mermaid: нечем рисовать"
fi

# Список прямых зависимостей закрыт намеренно. Решение живёт в закрытом
# контуре банка, и каждая новая зависимость это то, что придётся объяснять
# на защите и проверять на лицензию. Косвенные не считаем: они приходят
# вместе с разрешёнными и отдельного решения не требуют.
ALLOWED_DEPS="github.com/prometheus/client_golang golang.org/x/sync gopkg.in/yaml.v3 golang.org/x/time"
DIRECT="$(awk '/^require \(/,/^\)/' go.mod | grep -v '// indirect' | grep -oE '^\s+[a-z0-9.]+\.[a-z]+/[^ ]+' | tr -d '\t ' || true)"
EXTRA=""
for dep in $DIRECT; do
  case " $ALLOWED_DEPS " in
    *" $dep "*) ;;
    *) EXTRA="$EXTRA $dep" ;;
  esac
done
if [ -z "$EXTRA" ]; then
  ok "новых прямых зависимостей нет"
else
  bad "добавлены зависимости:$EXTRA"
fi

printf '\n\033[1m═══ Итог ═══\033[0m\n'
printf '  прошло %d, не прошло %d, пропущено %d\n\n' "$PASSED" "$FAILED" "$SKIPPED"

if [ "$FAILED" -gt 0 ]; then
  printf '\033[31mВОРОТА ЗАКРЫТЫ.\033[0m Правку вливать нельзя.\n'
  exit 1
fi
printf '\033[32mВОРОТА ОТКРЫТЫ.\033[0m\n'
exit 0
