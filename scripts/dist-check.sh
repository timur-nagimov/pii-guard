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
# Каталоги делятся на два разряда, и путать их нельзя.
#
# Первый разряд ищется где угодно: такое имя не бывает осмысленным исходником
# ни на какой глубине.
for bad in ".git/" "node_modules/" ".venv/" "venv/" "__pycache__/" ".idea/"; do
  if grep -q "^$bad\|/$bad" <<< "$LIST"; then echo "ПРОВАЛ: в архиве есть $bad"; fail=1; fi
done

# Второй разряд ищется ТОЛЬКО в корне. Эти имена законны глубже: cmd/corpus
# это инструмент сборки наборов данных из семи исходников, и он обязан ехать
# в архиве. Прежнее правило искало corpus/ на любой глубине и заворачивало
# архив из-за собственного исходного кода.
for bad in "target/" "build/" "dist/" "bin/" "obj/" "coverage/" "corpus/" "tools/"; do
  if grep -q "^$bad" <<< "$LIST"; then echo "ПРОВАЛ: в архиве есть корневой $bad"; fail=1; fi
done
# Файл с ключами не должен попасть, а пример без значений попасть обязан.
if grep -qE '(^|/)\.env$' <<< "$LIST"; then echo "ПРОВАЛ: в архиве есть файл с ключами .env"; fail=1; fi
if ! grep -qE '(^|/)\.env\.example$' <<< "$LIST"; then echo "ПРОВАЛ: нет примера настроек .env.example"; fail=1; fi
if grep -qE '\.(mp4|mov|png|jpg|zip|tar|gz|bin|exe)$' <<< "$LIST"; then echo "ПРОВАЛ: в архиве есть бинарные или медиафайлы"; fail=1; fi

# Скомпилированные файлы расширения обычно не имеют, поэтому ищем их по
# содержимому: распаковываем во временный каталог и смотрим тип каждого файла.
TMP=$(mktemp -d)
unzip -qq "$ZIP" -d "$TMP"
while IFS= read -r f; do
  case "$(file -b "$f")" in
    *executable*|*Mach-O*|*ELF*|*"archive data"*)
      echo "ПРОВАЛ: скомпилированный файл в архиве: ${f#$TMP/}"; fail=1;;
  esac
done < <(find "$TMP" -type f -size +100k)
BIG=$(find "$TMP" -type f -size +1M | head -5)
if [[ -n "$BIG" ]]; then echo "внимание, файлы больше мегабайта:"; sed "s#$TMP/#  #" <<< "$BIG"; fi
rm -rf "$TMP"
echo "файлов в архиве: $(wc -l <<< "$LIST")"
(( fail == 0 )) && echo "архив чистый"
exit $fail
