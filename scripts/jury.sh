#!/usr/bin/env bash
# Проверочные сценарии для жюри: по одному блоку на каждый критерий оценки.
# Запуск: scripts/jury.sh <адрес> <номер критерия|all>
set -o pipefail
URL="${1:-http://127.0.0.1:8080}"
WHICH="${2:-all}"
[[ -f .env ]] && set -a && . ./.env && set +a

JURY_KEY="${JURY_KEY:-}"; JURY2_KEY="${JURY2_KEY:-}"; KILO_KEY="${KILO_KEY:-}"
n=0

# Профили с ключами доступа выключаются, если ключ не задан. Без этой проверки
# показ выглядел бы так: «профиль жюри, частичная маска» и вывод, совпадающий с
# профилем по умолчанию, потому что запрос ушёл анонимной системе. Жюри увидело
# бы, что настройка ничего не меняет, и списало бы это решению в минус.
missing_keys=""
for pair in "JURY_KEY:профиль жюри" "JURY2_KEY:профиль без права демаскирования" "KILO_KEY:профиль с плейсхолдерами для модели"; do
  var="${pair%%:*}"; human="${pair#*:}"
  if [[ -z "${!var}" ]]; then
    missing_keys="${missing_keys}
    $var — $human"
  fi
done

if [[ -n "$missing_keys" ]]; then
  cat <<BANNER

  ВНИМАНИЕ. Не заданы ключи доступа, и показы по ним будут неполными:$missing_keys

  Сценарии ниже отработают, но профили с ключами покажут результат анонимной
  системы, а не свой. Так выглядит, будто настройка ничего не меняет.

  Чтобы увидеть их по-настоящему, задайте ключи в .env и перезапустите сервис.
  Хеш для настроек считается так:

    printf '%s' "ваш-ключ" | shasum -a 256 | cut -d' ' -f1

BANNER
fi

req() { # текст, идентификатор, [ключ]
  local body raw
  body=$(python3 -c 'import json,sys;print(json.dumps({"payload":sys.argv[1],"payload_id":sys.argv[2]}))' "$1" "$2")
  if [[ -n "${3:-}" ]]; then
    raw=$(curl -sk --max-time 15 -X POST "$URL/process" -H 'Content-Type: application/json' -H "X-System-Key: $3" -d "$body")
  else
    raw=$(curl -sk --max-time 15 -X POST "$URL/process" -H 'Content-Type: application/json' -d "$body")
  fi
  python3 -c 'import json,sys
raw=sys.stdin.read()
try:
    d=json.loads(raw)
except Exception:
    print(raw.strip()); sys.exit()
print(d.get("result", json.dumps(d, ensure_ascii=False)))' <<< "$raw"
}

show() { n=$((n+1)); printf '\n[%d] %s\n  вход:  %s\n  выход: %s\n' "$n" "$1" "$2" "$3"; }

case_1() {
  echo "=== Критерий 1. Качество идентификации и маскирования ==="
  local i=0
  while IFS= read -r t; do
    i=$((i+1)); show "предложение $i" "$t" "$(req "$t" "jury1-$i-$RANDOM")"
  done <<'TEXTS'
Клиент Иванов Иван Иванович, дата рождения 12.03.1985, место рождения г. Тула.
Паспорт серия 4509 номер 123456 выдан ОУФМС России по г. Москве 15.04.2005, код подразделения 770-001.
Гражданство Российская Федерация, водительское удостоверение 77 АА 123456.
Адрес регистрации: 101000, г. Москва, ул. Ленина, д. 5, кв. 12.
Контакты: телефон +7 (916) 123-45-67, электронная почта ivan.petrov@mail.ru.
ИНН 500100732259, СНИЛС 112-233-445 95.
Карта 4111 1111 1111 1111, держатель IVAN IVANOV, CVV 123, пин-код 4321.
Клиентка Смирнова Анна Петровна, родившаяся пятого марта 1990 года, проживает по адресу г. Казань, ул. Баумана, д. 7.
Contact John Smith, phone +7 916 123 45 67, email john.smith@example.com, card 5555 5555 5555 4444.
Заявитель: Петров-Водкин Пётр Кузьмич, тел. 8-916-123-45-67, паспорт 4510 654321.
TEXTS
}

case_2() {
  echo "=== Критерий 2. Корректность демаскирования ==="
  local t="Клиент Иванов Иван Иванович, паспорт 4509 123456" id="jury2-$RANDOM"
  local m; m=$(req "$t" "$id")
  show "маскирование" "$t" "$m"
  local b; b=$(req "$m" "$id")
  show "обратное преобразование" "$m" "$b"
  [[ "$b" == "$t" ]] && echo "  ✓ совпало с исходной строкой побайтово" || echo "  ✗ НЕ совпало"
  local id2="jury2b-$RANDOM" m2
  m2=$(req "$t" "$id2" "$JURY2_KEY")
  show "система без права демаскирования: маска" "$t" "$m2"
  show "она же пытается демаскировать" "$m2" "$(req "$m2" "$id2" "$JURY2_KEY")"
  echo "  ожидается отказ demask_forbidden"
}

case_3() {
  echo "=== Критерий 3. Точность отнесения и устойчивость к вариациям ==="
  local i=0
  while IFS= read -r t; do
    i=$((i+1)); show "ловушка $i" "$t" "$(req "$t" "jury3-$i-$RANDOM")"
  done <<'TEXTS'
Поэт Александр Пушкин родился в Москве в 1799 году, памятник ему стоит на Тверской.
Отделение банка расположено по адресу: г. Москва, ул. Тверская, д. 1, режим работы с 9:00.
Клиент Пушкин Александр Сергеевич, паспорт 4509 123456, обратился в отделение.
Встреча состоится на улице Пушкина у библиотеки имени Тургенева.
КЛИЕНТ ИВАНОВ ИВАН ИВАНОВИЧ, ПАСПОРТ 4509 123456, ТЕЛЕФОН +7 916 123 45 67
клиент иванов иван иванович, паспорт 4509 123456
Дата рождения 03.12.1985, дата в другом порядке 1985-12-03, дата текстом двенадцатое марта 1985 года.
Паспорт: серия 45 09 № 123456. Другая запись: 4509123456.
Сумма заказа 4509 рублей по договору 12345678 от 12.03.2025, оплата прошла.
Срок действия карты 12/27, код из сообщения 123456 никому не сообщайте.
TEXTS
}

case_4() {
  echo "=== Критерий 4. Гибкая настройка и расширяемость ==="
  echo "Файл настроек: configs/config.yaml. Изменения применяются без перезапуска."
  local t="Клиент Иванов Иван Иванович, тел. +7 916 123 45 67, карта 4111 1111 1111 1111"
  show "профиль по умолчанию (полная маска)" "$t" "$(req "$t" "jury4a-$RANDOM")"
  show "профиль жюри (частичная маска, ФИО инициалами)" "$t" "$(req "$t" "jury4b-$RANDOM" "$JURY_KEY")"
  echo
  echo "  Дальше проверяется руками, каждый шаг занимает меньше минуты:"
  echo "  1) отключить систему:      в configs/config.yaml поставить enabled: false у jury_demo → запрос с её ключом получает 403"
  echo "  2) убрать тип:             убрать PHONE из types у jury_demo → телефон перестаёт маскироваться"
  echo "  3) запретить демаскирование: demask: false → обратное преобразование получает 403"
  echo "  4) добавить новый тип:     дописать блок в custom_types → тип начинает находиться"
  echo "  После правки: kill -HUP <pid> либо подождать пять секунд, перезапуск не нужен."
}

case_5() {
  echo "=== Критерий 5. Производительность ==="
  local t="Клиент Иванов Иван Иванович, паспорт 4509 123456, тел. +7 916 123 45 67"
  local start end
  start=$(python3 -c 'import time;print(time.time())')
  req "$t" "jury5-$RANDOM" >/dev/null
  end=$(python3 -c 'import time;print(time.time())')
  python3 -c "print('  задержка одного запроса: %.1f мс' % (($end-$start)*1000))"
  echo "  доли задержки из показателей сервиса:"
  curl -sk --max-time 10 "$URL/metrics" | grep -E '^pii_request_duration_seconds_(bucket|count|sum)' | tail -5 | sed 's/^/    /'
  echo "  большой текст (около ста тысяч токенов):"
  python3 -c '
import json,sys,time,urllib.request
base="Клиент Иванов Иван Иванович, паспорт 4509 123456, тел. +7 916 123 45 67. "
text=base*5500
t=time.time()
req=urllib.request.Request(sys.argv[1]+"/process",data=json.dumps({"payload":text,"payload_id":"jury5-big"}).encode(),headers={"Content-Type":"application/json"})
r=json.loads(urllib.request.urlopen(req,timeout=30).read())
print("    размер %d КБ, обработано за %.0f мс, изменено символов: %d" % (len(text.encode())/1024,(time.time()-t)*1000,sum(1 for a,b in zip(text,r["result"]) if a!=b)))' "$URL"
}

case_6() {
  echo "=== Критерий 6. Безопасность, журнал и показатели ==="
  local t="Клиент Иванов Иван Иванович, карта 4111 1111 1111 1111"
  show "запрос" "$t" "$(req "$t" "jury6-$RANDOM")"
  echo "  журнал этого запроса (типы есть, значений нет): make logs | tail -3"
  echo "  показатели по типам:"
  curl -sk --max-time 10 "$URL/metrics" | grep '^pii_detections_total' | head -8 | sed 's/^/    /'
  echo "  неверное тело запроса:"
  curl -sk --max-time 10 -X POST "$URL/process" -H 'Content-Type: application/json' -d '{"payload":123}' | sed 's/^/    /'
  echo "  неизвестный ключ доступа:"
  curl -sk --max-time 10 -X POST "$URL/process" -H 'Content-Type: application/json' -H 'X-System-Key: неверный' -d '{"payload":"тест","payload_id":"x"}' | sed 's/^/    /'
}

case_7() {
  echo "=== Критерий 7. Расширенные сценарии ==="
  local t="Клиент Иванов Иван Иванович, карта 4111 1111 1111 1111, пин-код 4321"
  show "плейсхолдеры для языковой модели (система kilo)" "$t" "$(req "$t" "jury7a-$RANDOM" "$KILO_KEY")"
  show "свой вид маски для системы жюри" "$t" "$(req "$t" "jury7b-$RANDOM" "$JURY_KEY")"
  show "дополнительные документы" "СНИЛС 112-233-445 95, загранпаспорт 75 1234567, вид на жительство 82 № 0123456" "$(req "СНИЛС 112-233-445 95, загранпаспорт 75 1234567, вид на жительство 82 № 0123456" "jury7c-$RANDOM")"
  show "пин-код без карты" "Пин-код 4321 запомните" "$(req "Пин-код 4321 запомните" "jury7d-$RANDOM")"
  echo "  правило сочетаний включается признаком context_rules_enabled в настройках системы"

  echo
  echo "--- Разбор с объяснением решения ---"
  echo "  По каждому фрагменту видно, КАКОЕ ПРАВИЛО сработало, а не только результат."
  curl -s -m 10 -X POST "$URL/v1/inspect" -H 'Content-Type: application/json' \
    -d '{"text":"Клиент Иванов Иван Иванович, паспорт 4509 123456, тел +7 916 123-45-67"}' \
    | python3 -c '
import json, sys
d = json.load(sys.stdin)
for s in d.get("spans", []):
    print("    %-12s %-26s уверенность %s  правило %s" % (s["type"], repr(s["value"]), s["confidence"], s["reason"]))
print("    снято с маскирования фрагментов: %d" % len(d.get("skipped", [])))
' 2>/dev/null || echo "    разбор выключен настройкой inspect_enabled"

  echo
  echo "--- Связи между фрагментами одного человека ---"
  echo "  Текст с двумя людьми разбирается на двух субъектов, карта остаётся у своего."
  curl -s -m 10 -X POST "$URL/v1/inspect" -H 'Content-Type: application/json' \
    -d '{"text":"Клиент Иванов Иван Иванович, карта 4111 1111 1111 1111. Менеджер Петров Пётр, тел +7 495 111-22-33."}' \
    | python3 -c '
import json, sys
d = json.load(sys.stdin)
sp = d.get("spans", [])
subs = d.get("subjects", [])
if not subs:
    print("    субъектов ноль: связи строятся только при context_rules_enabled: true")
for i, s in enumerate(subs, 1):
    vals = [sp[j]["value"] for j in s.get("fragments", []) if j < len(sp)]
    print("    субъект %d: типы %s" % (i, s.get("types")))
    print("       фрагменты: %s" % vals)
' 2>/dev/null || true

  echo
  echo "--- Матрица покрытия: что настроено, а что нет ---"
  curl -s -m 10 "$URL/v1/coverage" -H "X-System-Key: $JURY_KEY" \
    | python3 -c '
import json, sys
d = json.load(sys.stdin)
rows = d.get("rows") or d.get("types") or []
systems = d.get("systems", [])
print("    систем-потребителей: %d, типов в матрице: %d" % (len(systems), len(rows)))
' 2>/dev/null || echo "    матрица недоступна"

  echo
  echo "  Наглядно всё перечисленное на одной странице: $URL/ui"
}

case_8() {
  echo "=== Критерий 8. Демонстрация ==="
  echo "  Схема цепочки и объяснение работы: README.md, раздел «Как устроено»."
  echo "  Формат маскирования: docs/MASK-FORMAT.md."
  echo "  Ограничения и планы: README.md, последний раздел."
}

case "$WHICH" in
  1) case_1;; 2) case_2;; 3) case_3;; 4) case_4;;
  5) case_5;; 6) case_6;; 7) case_7;; 8) case_8;;
  all) case_1; case_2; case_3; case_4; case_5; case_6; case_7; case_8;;
  *) echo "укажите номер критерия от 1 до 8 либо all"; exit 1;;
esac
