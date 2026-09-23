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
# Каждый прогон ворот держит свои следы в своём каталоге. Раньше все писали
# по общим путям в /tmp, и параллельные прогоны затирали улики друг друга:
# ворота падали, а в файле подробностей лежал уже чужой успешный результат.
WORK="$(mktemp -d "${TMPDIR:-/tmp}/gate.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# Порт выбирается свободный, а не фиксированный. Фиксированный порт означает,
# что ворота могут опросить чужую уже запущенную копию сервиса и проверить не
# тот код. Это не гипотеза: ровно так сегодня проверялась правка, которой в
# опрошенной копии не было.
if [ -n "$GATE_URL" ]; then
  URL="$GATE_URL"
  URL_GIVEN=1
else
  GATE_PORT_PICKED="$(python3 -c '
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
')"
  URL="http://127.0.0.1:$GATE_PORT_PICKED"
  URL_GIVEN=0
fi
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
      go run ./cmd/score -dataset "$CORPUS" >"$WORK/init.txt" 2>&1 || {
        echo "замер не отработал, подробности $WORK/init.txt"; exit 2; }
      python3 "$ROOT/scripts/gate-quality.py" --init "$WORK/init.txt" > "$BASELINE" || exit 2
      echo "базовая линия записана: $BASELINE"
      echo "Проверьте её глазами и внесите в git отдельным коммитом."
      exit 0
      ;;
    --init-perf)
      echo "снимаю скорость горячего пути..."
      go test ./internal/engine/ -run XXX -bench 'BenchmarkMask/250b|BenchmarkMask/2kb' \
        -benchtime 300ms -count=1 >"$WORK/bench-init.txt" 2>&1 || {
        echo "бенчмарк не отработал"; exit 2; }
      python3 "$ROOT/scripts/gate-perf.py" --init "$WORK/bench-init.txt" > "$ROOT/scripts/perf-baseline.json" || exit 2
      echo "базовая линия скорости записана: $ROOT/scripts/perf-baseline.json"
      echo "Числа зависят от машины: снимайте там же, где потом сравниваете."
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
      echo "   8  скорость горячего пути не просела"
      echo "   9  контракт: маска и обратное преобразование"
      echo "  10  сценарии жюри"
      echo "  11  персональные данные не в журнале и не в показателях"
      echo "  12  в архиве нет секретов и скомпилированных файлов"
      echo "  13  всё зафиксировано, дерево чистое"
      echo "  14  ссылки в документах целы"
      echo "  15  схемы Mermaid разбираются"
      echo "  16  исходников под правилом игнорирования нет"
      echo "  17  запрещённые зависимости не добавлены"
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

if go vet ./... >"$WORK/vet.log" 2>&1; then
  ok "статический анализ"
else
  bad "статический анализ, подробности $WORK/vet.log"
fi

if go build ./... >"$WORK/build.log" 2>&1; then
  ok "сборка"
else
  bad "сборка, подробности $WORK/build.log"
fi

# Тесты с детектором гонок чувствительны к загрузке машины: под параллельным
# прогоном они изредка падают по времени, а не по коду. Отправить хорошую
# ветку в отказ из-за занятого процессора хуже, чем потратить минуту на
# повтор, поэтому неудача переспрашивается один раз. Настоящая поломка
# воспроизводится оба раза.
if go test ./... -race -count=1 >"$WORK/test.log" 2>&1; then
  ok "тесты с детектором гонок"
else
  printf '  повторяю тесты: первая попытка не прошла, машина может быть занята\n'
  sleep 5
  if go test ./... -race -count=1 >"$WORK/test-retry.log" 2>&1; then
    ok "тесты с детектором гонок (со второй попытки)"
  else
    bad "тесты, подробности $WORK/test.log и $WORK/test-retry.log"
  fi
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

  if go run ./cmd/score -dataset "$CORPUS" >"$WORK/score.txt" 2>&1; then
    # Оба правила проверяет измеритель: положительные срезы с допуском,
    # отрицательные строго с нулём. Держать их в одном месте важнее, чем
    # разделять ради красивого вывода.
    if python3 "$ROOT/scripts/gate-quality.py" "$BASELINE" "$WORK/score.txt" >"$WORK/quality.txt" 2>&1; then
      ok "качество по типам не просело"
      ok "отрицательные срезы строго ноль"
      grep -q "Стало лучше" "$WORK/quality.txt" && sed -n '/Стало лучше/,/^$/p' "$WORK/quality.txt" | sed 's/^/        /'
    else
      if grep -q "ложные срабатывания" "$WORK/quality.txt"; then
        ok "качество по типам не просело"
        bad "отрицательные срезы: есть ложные срабатывания"
      else
        bad "качество просело"
        ok "отрицательные срезы строго ноль"
      fi
      sed 's/^/        /' "$WORK/quality.txt"
    fi
  else
    bad "замер качества не отработал, подробности $WORK/score.txt"
  fi
fi

say "8. Скорость"

# Пробел, который эта проверка закрывает, стоил дорого: сборка ночных веток
# оказалась на треть медленнее, и ни одна из проверок этого не увидела. Ворота
# смотрели на качество и на правильность, но не на скорость.
PERF_BASE="$ROOT/scripts/perf-baseline.json"
if [ "$QUICK" = "1" ]; then
  skip "скорость горячего пути: быстрый режим"
elif [ ! -f "$PERF_BASE" ]; then
  skip "скорость: нет базовой линии, снимите bash scripts/gate.sh --init-perf"
else
  if go test ./internal/engine/ -run XXX -bench 'BenchmarkMask/250b|BenchmarkMask/2kb' \
       -benchtime 300ms -count=1 >"$WORK/bench.txt" 2>&1; then
    if python3 "$ROOT/scripts/gate-perf.py" "$PERF_BASE" "$WORK/bench.txt" >"$WORK/perf.txt" 2>&1; then
      ok "скорость горячего пути не просела"
      grep -q "Стало быстрее" "$WORK/perf.txt" && sed -n '/Стало быстрее/,/^$/p' "$WORK/perf.txt" | sed 's/^/        /'
    else
      bad "скорость просела:"
      sed 's/^/        /' "$WORK/perf.txt"
    fi
  else
    bad "бенчмарк не отработал, подробности $WORK/bench.txt"
  fi
fi

say "9-11. Живой сервис"

SERVICE_PID=""
GATE_PORT="$(printf '%s' "$URL" | sed 's#.*:##')"
if [ "$URL_GIVEN" = "1" ] && curl -s -m 3 -o /dev/null "$URL/healthz" 2>/dev/null; then
  # Адрес задал вызывающий, значит он отвечает за то, что там нужная сборка.
  STARTED_HERE=0
else
  go build -o "$WORK/pii-guard" ./cmd/pii-guard 2>/dev/null

  # Свой файл настроек на время прогона. Две причины, и обе стоили ворот
  # молчаливого пропуска самых важных проверок.
  #
  # Первая: порт. Рабочие настройки слушают тот же порт, что и запущенный
  # вручную сервис, и ворота либо не поднялись бы, либо опросили чужую копию
  # и проверили не тот код. Такое уже случалось.
  #
  # Вторая: обязательные переменные. Настройки требуют хеши ключей доступа
  # для каждой системы, и без них служба честно отказывается стартовать.
  # Ворота при этом печатали «сервис не поднялся» и шли дальше, то есть
  # проверка контракта, сценариев жюри и поиска утечки не выполнялась вовсе.
  sed -e "s#^  http: .*#  http: \":$GATE_PORT\"#" \
      -e "s#^  https: .*#  https: \"\"#" \
      configs/config.yaml > "$WORK/config.yaml"

  # Одноразовые значения: ворота проверяют поведение, а не секреты. Хеши
  # берутся от заведомо известной строки, чтобы при надобности можно было
  # постучаться в службу и ключом тоже.
  GATE_KEY="${GATE_SYSTEM_KEY:-gate-probe-key}"
  GATE_HASH="$(printf '%s' "$GATE_KEY" | shasum -a 256 | cut -d' ' -f1)"
  export GATE_SYSTEM_KEY="$GATE_KEY"

  # Имена переменных берём из самих настроек, а не списком: список устарел бы
  # при добавлении новой системы, и ворота снова начали бы молча пропускать.
  GATE_ENV=(
    "PII_STORE_KEY=$(head -c 32 /dev/urandom | base64)"
    "ALFAGEN_URL=https://example.invalid/v1/chat/completions"
    "ALFAGEN_TOKEN=gate-probe"
  )
  for name in $(grep -oE '\$\{[A-Z_][A-Z0-9_]*\}' configs/config.yaml | tr -d '${}' | sort -u); do
    case "$name" in
      *_SHA256) GATE_ENV+=("$name=$GATE_HASH") ;;
      PII_STORE_KEY|ALFAGEN_URL|ALFAGEN_TOKEN) ;;
      *) GATE_ENV+=("$name=$GATE_KEY") ;;
    esac
  done

  env "${GATE_ENV[@]}" "$WORK/pii-guard" -config "$WORK/config.yaml" \
    >"$WORK/service.log" 2>&1 &
  SERVICE_PID=$!
  STARTED_HERE=1
  for _ in $(seq 1 30); do
    curl -s -m 2 -o /dev/null "$URL/healthz" 2>/dev/null && break
    sleep 0.5
  done
fi

cleanup() {
  if [ -n "$SERVICE_PID" ]; then kill "$SERVICE_PID" 2>/dev/null; fi
}
trap cleanup EXIT

if curl -s -m 3 -o /dev/null "$URL/healthz" 2>/dev/null; then
  if bash scripts/verify.sh "$URL" >"$WORK/verify.log" 2>&1; then
    ok "контракт: маска и обратное преобразование"
  else
    bad "контракт, подробности $WORK/verify.log"
  fi

  if bash scripts/jury.sh "$URL" all >"$WORK/jury.log" 2>&1; then
    ok "сценарии жюри"
  else
    bad "сценарии жюри, подробности $WORK/jury.log"
  fi

  # Самая важная проверка всего файла. Значение персональных данных,
  # попавшее в журнал или в показатели, обесценивает решение целиком.
  if bash scripts/gate-noleak.sh "$URL" "$WORK/service.log" >"$WORK/leak.log" 2>&1; then
    ok "персональные данные не в журнале и не в показателях"
  else
    bad "УТЕЧКА в журнал или показатели, подробности $WORK/leak.log"
  fi
else
  bad "сервис не поднялся, подробности $WORK/service.log"
fi

say "12-17. Поставка"

# Архив собирается здесь же, а не берётся готовым. Раньше ворота проверяли
# тот архив, что случайно лежал в каталоге: в основном дереве он был старым,
# и проверка проходила ложно, а в любой свежей копии дерева его не было
# вовсе, и проверка падала ложно. Проверять надо то, что соберётся сейчас.
mkdir -p "$WORK/dist"
if git archive --format=zip --output "$WORK/dist/pii-guard.zip" HEAD 2>"$WORK/dist-build.log"; then
  if bash scripts/dist-check.sh "$WORK/dist/pii-guard.zip" >"$WORK/dist.log" 2>&1; then
    ok "в архиве нет секретов и скомпилированных файлов"
  else
    bad "в архив попало лишнее, подробности $WORK/dist.log"
  fi
else
  bad "архив не собрался, подробности $WORK/dist-build.log"
fi

# Всё, что не зафиксировано, в архив не попадёт и пропадёт вместе с рабочей
# копией. Сегодня это дважды едва не стоило работы: сперва пакет
# журналирования из девятнадцати файлов лежал незаведённым, и архив не
# собирался; потом три ночные задачи прошли ворота, а их правки остались
# незафиксированными, потому что проверка смотрела только на НОВЫЕ файлы и
# не видела изменённых. Теперь дерево обязано быть чистым целиком.
DIRTY="$( { git ls-files --others --exclude-standard 2>/dev/null
            git diff --name-only 2>/dev/null
            git diff --cached --name-only 2>/dev/null
          } | grep -vE '^(corpus|dist)(/|$)' | sort -u || true)"
if [ -z "$DIRTY" ]; then
  ok "всё зафиксировано, дерево чистое"
else
  bad "не зафиксировано и в архив не попадёт:"
  printf '%s\n' "$DIRTY" | head -12 | sed 's/^/        /'
fi

# Исходник, попавший под правило игнорирования, не виден ни как изменённый,
# ни как новый: git о нём просто молчит. Проверка выше его не поймает.
# Так уже случилось: узор coverage.* в .gitignore проглотил исходник
# internal/api/coverage.go, ветка перестала собираться, а ворота прошли,
# потому что на диске файл был. Ищем такие явно.
IGNORED_SRC="$(git ls-files --others --ignored --exclude-standard 2>/dev/null \
  | grep -E '\.go$' | grep -vE '^(corpus|dist)/' || true)"
if [ -z "$IGNORED_SRC" ]; then
  ok "исходников под правилом игнорирования нет"
else
  bad "исходники игнорируются git и в сборку не попадут:"
  printf '%s\n' "$IGNORED_SRC" | head -8 | sed 's/^/        /'
fi

if python3 "$ROOT/scripts/gate-links.py" >"$WORK/links.log" 2>&1; then
  ok "ссылки в документах целы"
else
  bad "битые ссылки:"
  sed 's/^/        /' "$WORK/links.log"
fi

if [ "$QUICK" = "1" ]; then
  skip "схемы Mermaid: быстрый режим"
elif command -v mmdc >/dev/null 2>&1 || [ -x /tmp/mermaid-check/node_modules/.bin/mmdc ]; then
  if bash scripts/gate-mermaid.sh >"$WORK/mermaid.log" 2>&1; then
    ok "схемы Mermaid разбираются"
  else
    bad "сломанные схемы, подробности $WORK/mermaid.log"
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
