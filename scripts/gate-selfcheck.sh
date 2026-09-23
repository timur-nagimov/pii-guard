#!/usr/bin/env bash
# Проверка самих ворот: ловят ли они то, что обещают ловить.
#
# Зачем это нужно. Проверка, которая всегда зелёная, неотличима от проверки,
# которая ничего не проверяет. В этом проекте такое уже случалось дважды:
# проверка на утечку печатала «утечек нет» при недоступном сервисе, а тест
# вытеснения смотрел на счётчик вместо самих записей и пропускал потерю 255
# записей разом. Оба выглядели защитой и не защищали.
#
# Способ: в каждую проверку подсовывается заведомая поломка, и она обязана
# покраснеть. Поломки подделываются данными, а не правкой кода: скрипт ничего
# в репозитории не меняет.
#
# Запуск:  bash scripts/gate-selfcheck.sh
# Ноль означает, что все проверки ворот работают.

set -o pipefail

cd "$(dirname "$0")/.." || exit 2
ROOT="$PWD"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/selfcheck.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

PASSED=0
FAILED=0

ok()  { PASSED=$((PASSED+1)); printf '  \033[32mok\033[0m    %s\n' "$1"; }
bad() { FAILED=$((FAILED+1)); printf '  \033[31mПЛОХО\033[0m %s\n' "$1"; }

# expect ИМЯ ОЖИДАЕМЫЙ_КОД КОМАНДА...
expect() {
  local name="$1" want="$2"; shift 2
  "$@" >/dev/null 2>&1
  local got=$?
  if [ "$got" = "$want" ]; then ok "$name"; else bad "$name: код $got, ожидался $want"; fi
}

printf '\n\033[1m── Качество и отрицательные срезы\033[0m\n'

CORPUS="${GATE_CORPUS:-corpus/dataset.jsonl}"
if [ ! -f "$CORPUS" ]; then
  go run ./cmd/gen -out "$CORPUS" -n 8000 -seed 42 >/dev/null 2>&1
fi
go run ./cmd/score -dataset "$CORPUS" >"$WORK/real.txt" 2>&1

python3 - "$WORK" <<'PY'
import pathlib, re, sys
w = pathlib.Path(sys.argv[1])
src = w.joinpath("real.txt").read_text(encoding="utf-8")

# Просадка одного типа заметно ниже допуска.
drop = re.sub(r"^(FIO\s+)(\d+\.\d+)",
              lambda m: m.group(1) + f"{float(m.group(2))-0.2:.4f}", src, flags=re.M)
w.joinpath("drop.txt").write_text(drop, encoding="utf-8")

# Ложное срабатывание в отрицательном срезе: он обязан быть строго нулевым.
neg = re.sub(r"^(neg_\w+\s+)(\d+\.\d+)",
             lambda m: m.group(1) + "0.0300", src, count=1, flags=re.M)
w.joinpath("neg.txt").write_text(neg, encoding="utf-8")
PY

expect "настоящий замер принимается"            0 python3 "$ROOT/scripts/gate-quality.py" scripts/baseline.json "$WORK/real.txt"
expect "просадка типа ловится"                  1 python3 "$ROOT/scripts/gate-quality.py" scripts/baseline.json "$WORK/drop.txt"
expect "ложное срабатывание ловится"            1 python3 "$ROOT/scripts/gate-quality.py" scripts/baseline.json "$WORK/neg.txt"

printf '\n\033[1m── Скорость\033[0m\n'

python3 - "$WORK" "$ROOT" <<'PY'
import json, pathlib, sys
w, root = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
base = json.loads(root.joinpath("scripts/perf-baseline.json").read_text(encoding="utf-8"))
same, slow = [], []
for name, ns in base["замеры"].items():
    same.append(f"{name}-12\t1000\t{ns:.0f} ns/op")
    slow.append(f"{name}-12\t1000\t{ns*2:.0f} ns/op")
w.joinpath("perf-same.txt").write_text("\n".join(same) + "\n", encoding="utf-8")
w.joinpath("perf-slow.txt").write_text("\n".join(slow) + "\n", encoding="utf-8")
PY

expect "прежняя скорость принимается"           0 python3 "$ROOT/scripts/gate-perf.py" scripts/perf-baseline.json "$WORK/perf-same.txt"
expect "замедление вдвое ловится"               1 python3 "$ROOT/scripts/gate-perf.py" scripts/perf-baseline.json "$WORK/perf-slow.txt"

printf '\n\033[1m── Утечка персональных данных\033[0m\n'

# Поднимаем сервис на свободном порту со своими настройками.
PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
sed -e "s#^  http: .*#  http: \":$PORT\"#" -e "s#^  https: .*#  https: \"\"#" configs/config.yaml > "$WORK/config.yaml"
go build -o "$WORK/pii-guard" ./cmd/pii-guard 2>/dev/null
env PII_STORE_KEY="$(head -c 32 /dev/urandom | base64)" \
  "$WORK/pii-guard" -config "$WORK/config.yaml" >"$WORK/service.log" 2>&1 &
SVC=$!
trap 'kill $SVC 2>/dev/null; rm -rf "$WORK"' EXIT
for _ in $(seq 1 30); do
  curl -s -m 2 -o /dev/null "http://127.0.0.1:$PORT/healthz" 2>/dev/null && break
  sleep 0.5
done

# Подложный журнал с настоящим значением из набора проверки.
printf '{"msg":"обработка","payload":"Гремислав Аполлинариевич Кудринцев"}\n' > "$WORK/leaky.log"

expect "чистый журнал принимается"              0 bash scripts/gate-noleak.sh "http://127.0.0.1:$PORT" "$WORK/service.log"
expect "утечка в журнал ловится"                1 bash scripts/gate-noleak.sh "http://127.0.0.1:$PORT" "$WORK/leaky.log"
expect "отсутствие журнала не считается успехом" 2 bash scripts/gate-noleak.sh "http://127.0.0.1:$PORT" "$WORK/нет-такого-файла"

printf '\n\033[1m── Контракт\033[0m\n'
expect "рабочий сервис проходит контракт"       0 bash scripts/verify.sh "http://127.0.0.1:$PORT"

printf '\n\033[1m── Ссылки и схемы\033[0m\n'
expect "целые ссылки принимаются"               0 python3 "$ROOT/scripts/gate-links.py"

printf '\n\033[1m═══ Итог ═══\033[0m\n'
printf '  работает проверок: %d, не работает: %d\n\n' "$PASSED" "$FAILED"

if [ "$FAILED" -gt 0 ]; then
  printf '\033[31mЧАСТЬ ВОРОТ НЕ ЛОВИТ ТО, ЧТО ОБЕЩАЕТ.\033[0m Им верить нельзя.\n'
  exit 1
fi
printf '\033[32mВСЕ ПРОВЕРКИ ВОРОТ РАБОТАЮТ.\033[0m\n'
