#!/usr/bin/env bash
# Короткая проверка контракта: маскирование, повтор, обратное преобразование.
set -euo pipefail
URL="${1:-http://127.0.0.1:8080}"
ID="verify-$$"
TEXT='Клиент Иванов Иван Иванович, паспорт 4509 123456, тел. +7 (916) 123-45-67'

post() { curl -sk --max-time 10 -X POST "$URL/process" -H 'Content-Type: application/json' -d "$1"; }
jq_result() { python3 -c 'import json,sys;print(json.load(sys.stdin)["result"])'; }

fail=0
MASK=$(post "$(python3 -c 'import json,sys;print(json.dumps({"payload":sys.argv[1],"payload_id":sys.argv[2]}))' "$TEXT" "$ID")" | jq_result)
echo "маска:   $MASK"
[[ "$MASK" != "$TEXT" ]] && echo "  ок: текст изменён" || { echo "  ПРОВАЛ: текст не изменён"; fail=1; }

AGAIN=$(post "$(python3 -c 'import json,sys;print(json.dumps({"payload":sys.argv[1],"payload_id":sys.argv[2]}))' "$TEXT" "$ID")" | jq_result)
[[ "$AGAIN" == "$MASK" ]] && echo "  ок: повтор идемпотентен" || { echo "  ПРОВАЛ: повтор дал другую маску"; fail=1; }

BACK=$(post "$(python3 -c 'import json,sys;print(json.dumps({"payload":sys.argv[1],"payload_id":sys.argv[2]}))' "$MASK" "$ID")" | jq_result)
[[ "$BACK" == "$TEXT" ]] && echo "  ок: обратное преобразование побайтово точно" || { echo "  ПРОВАЛ: обратное преобразование не совпало"; echo "  получено: $BACK"; fail=1; }

CODE=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$URL/process" -H 'Content-Type: application/json' -d '{"payload":123}')
[[ "$CODE" == "422" ]] && echo "  ок: неверное тело даёт 422" || { echo "  ПРОВАЛ: неверное тело дало $CODE"; fail=1; }

exit $fail
