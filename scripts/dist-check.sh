#!/usr/bin/env bash
# Проверка архива с исходным кодом перед загрузкой.
set -euo pipefail
ZIP="${1:-dist/pii-guard.zip}"
[[ -f "$ZIP" ]] || { echo "архив не найден: $ZIP"; exit 1; }

SIZE_KB=$(( $(wc -c < "$ZIP") / 1024 ))
echo "размер архива: ${SIZE_KB} КБ"
fail=0
(( SIZE_KB > 20480 )) && { echo "ПРОВАЛ: архив больше двадцати мегабайт"; fail=1; }

LIST=$(unzip -Z1 "$ZIP")
for bad in ".git/" "node_modules/" ".venv/" "venv/" "target/" "build/" "dist/" "bin/" "obj/" "__pycache__/" "coverage" ".idea/" "corpus/" ".env"; do
  if grep -q "$bad" <<< "$LIST"; then echo "ПРОВАЛ: в архиве есть $bad"; fail=1; fi
done
if grep -qE '\.(mp4|mov|png|jpg|zip|tar|gz|bin|exe)$' <<< "$LIST"; then echo "ПРОВАЛ: в архиве есть бинарные или медиафайлы"; fail=1; fi
echo "файлов в архиве: $(wc -l <<< "$LIST")"
(( fail == 0 )) && echo "архив чистый"
exit $fail
