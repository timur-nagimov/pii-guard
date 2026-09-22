#!/usr/bin/env bash
# Проверка на утечку персональных данных в журнал и показатели.
#
# Это самая важная проверка из всех ворот. Всё остальное чинится правкой,
# а значение персональных данных, утекшее в журнал, обесценивает решение
# целиком: журнал уезжает во внешний сбор, ложится в резервные копии и
# доступен тем, кому доступ к персональным данным не выдавали.
#
# Проверка идёт от обратного. Мы не спрашиваем «фильтрует ли код», мы шлём
# заведомо узнаваемые значения и ищем их в том, что сервис отдал наружу.
# Так ловится и то, о чём автор фильтра не подумал.
#
# Использование:
#   bash scripts/gate-noleak.sh <url> [файл журнала]
#
# Без файла журнала проверяются только показатели, и это отмечается в выводе:
# молча сокращать проверку нельзя, иначе ворота врут.

set -o pipefail

URL="${1:-http://127.0.0.1:18081}"
LOGFILE="${2:-}"
KEY="${GATE_SYSTEM_KEY:-}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Значения подобраны так, чтобы каждое было узнаваемо и не встречалось в
# коде, в служебных полях и в обычных словах. Редкая фамилия, необычный
# домен, неправильные контрольные суммы: жюри тоже подставляет выдуманные
# номера, и полагаться на контрольную сумму нельзя.
cat > "$TMP/values.txt" <<'EOF'
Гремислав Аполлинариевич Кудринцев
4276380012345678
987
9182
4509123456
770-055
МВД по Республике Марий Эл
12.03.1987
Йошкар-Ола
524905738261
16405912384
77 АХ 918273
zhmurkin.gremislav@postbox-rare.example
+7 916 555-84-19
город Йошкар-Ола улица Первомайская дом 42 квартира 17
425000
Кудринцев Г А
EOF

# Один текст со всеми значениями сразу: так проверяется и то, что фильтр не
# ломается на длинном тексте с плотной разметкой.
python3 - "$TMP" <<'PY'
import json, sys, pathlib
tmp = pathlib.Path(sys.argv[1])
vals = tmp.joinpath("values.txt").read_text(encoding="utf-8").strip().split("\n")
text = (
    "Анкета клиента. ФИО: {0}. Паспорт {4}, выдан {6} {7}, код подразделения {5}. "
    "Место рождения: {8}. Дата рождения {7}. СНИЛС {9}, ИНН {10}. "
    "Водительское удостоверение {11}. Адрес: {14}, индекс {15}. "
    "Почта {12}, телефон {13}. Карта {1}, CVV {2}, пин {3}, держатель {16}."
).format(*vals)
tmp.joinpath("payload.json").write_text(
    json.dumps({"payload_id": "gate-noleak-check-0001", "payload": text}, ensure_ascii=False),
    encoding="utf-8")
tmp.joinpath("inspect.json").write_text(
    json.dumps({"text": text}, ensure_ascii=False), encoding="utf-8")
# Отдельный запрос, где персональные данные положены в идентификатор запроса:
# это поле приходит от клиента и не раз оказывалось обходным путём.
tmp.joinpath("sneaky.json").write_text(
    json.dumps({"payload_id": vals[1], "payload": "обычный текст без данных"}, ensure_ascii=False),
    encoding="utf-8")
PY

AUTH=()
if [ -n "$KEY" ]; then AUTH=(-H "X-System-Key: $KEY"); fi

curl -s -m 20 -o "$TMP/r1.json" -X POST "$URL/process" \
  -H 'Content-Type: application/json' "${AUTH[@]}" --data-binary "@$TMP/payload.json"
curl -s -m 20 -o "$TMP/r2.json" -X POST "$URL/v1/inspect" \
  -H 'Content-Type: application/json' "${AUTH[@]}" --data-binary "@$TMP/inspect.json"
curl -s -m 20 -o "$TMP/r3.json" -X POST "$URL/process" \
  -H 'Content-Type: application/json' "${AUTH[@]}" --data-binary "@$TMP/sneaky.json"

# Показателям нужно время долететь до сборщика внутри процесса.
sleep 1
curl -s -m 20 -o "$TMP/metrics.txt" "$URL/metrics"

python3 - "$TMP" "$LOGFILE" <<'PY'
import pathlib, re, sys

tmp = pathlib.Path(sys.argv[1])
logfile = sys.argv[2] if len(sys.argv) > 2 else ""

values = [v for v in tmp.joinpath("values.txt").read_text(encoding="utf-8").strip().split("\n") if v]

# Ищем не только значение целиком, но и его куски. Утечка по частям это
# тоже утечка: по половине номера карты и по имени человек опознаётся.
needles = set()
for v in values:
    needles.add(v)
    for part in re.split(r"[\s,./-]+", v):
        if len(part) >= 6:
            needles.add(part)

# Короткое число вроде кода безопасности встречается внутри посторонних чисел
# (в показателях рантайма, в отметках времени), поэтому оно ищется по границам
# цифр, а не подстрокой: иначе ворота краснеют на пустом месте.
def hit(needle, body):
    if needle.isdigit() and len(needle) < 6:
        return re.search(r"(?<!\d)" + needle + r"(?!\d)", body) is not None
    return needle in body


channels = {"показатели": tmp.joinpath("metrics.txt")}
if logfile:
    # Файл журнала запрошен, но его нет. Это НЕ повод сказать «утечек нет»:
    # проверка просто не состоялась, а молчаливо сокращённая проверка хуже
    # отсутствующей, потому что её результату верят. Тот же разряд ошибки
    # уже находили в этом файле для канала показателей.
    if not pathlib.Path(logfile).exists():
        print(f"файла журнала нет: {logfile}")
        print("Проверка не состоялась. Считать её пройденной нельзя.")
        sys.exit(2)
    channels["журнал"] = pathlib.Path(logfile)
else:
    # Журнал не запрашивали вовсе: вызывающий знает, что проверяет только
    # показатели. Говорим об этом вслух, но не падаем.
    print("ВНИМАНИЕ: файл журнала не задан, проверены только показатели.")

leaks = []
for name, path in channels.items():
    try:
        body = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        # Непрочитанный канал это не «утечек нет», а несостоявшаяся проверка.
        print(f"не прочитать {name}: {exc}")
        sys.exit(2)
    for needle in sorted(needles):
        if hit(needle, body):
            for line in body.split("\n"):
                if hit(needle, line):
                    leaks.append((name, needle, line.strip()[:200]))
                    break

if leaks:
    print(f"НАЙДЕНА УТЕЧКА, совпадений {len(leaks)}:")
    for name, needle, line in leaks:
        print(f"  [{name}] {needle!r}")
        print(f"      {line}")
    sys.exit(1)

print(f"Утечек нет. Проверено каналов {len(channels)}, искомых значений {len(needles)}.")
sys.exit(0)
PY
