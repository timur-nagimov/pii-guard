#!/usr/bin/env bash
# Проверка схем Mermaid настоящим разбором.
#
# Схема, которая не рисуется, на GitHub выглядит как кусок непонятного текста
# посреди документа. Заметить это чтением нельзя: синтаксис похож на рабочий.
# Поэтому каждая схема прогоняется через тот же разборщик, которым рисует
# GitHub, а не просматривается глазами.
#
# Самая частая причина поломки: круглые скобки и запятые внутри подписи узла
# без кавычек. Правильно A["Текст (со скобкой), с запятой"], неправильно
# A[Текст (со скобкой), с запятой].
#
# Разборщик ставится во временный каталог вне репозитория: тащить его в
# зависимости проекта ради проверки документации незачем.

set -o pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${MERMAID_WORK:-/tmp/mermaid-check}"
MMDC=""

if command -v mmdc >/dev/null 2>&1; then
  MMDC="$(command -v mmdc)"
elif [ -x "$WORK/node_modules/.bin/mmdc" ]; then
  MMDC="$WORK/node_modules/.bin/mmdc"
else
  echo "Разборщик схем не найден. Поставить одной командой:"
  echo "  mkdir -p $WORK && cd $WORK && npm i @mermaid-js/mermaid-cli"
  echo "Без него проверка схем пропускается, а не считается пройденной."
  exit 2
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

python3 - "$ROOT" "$TMP" <<'PY'
import pathlib, re, sys

root = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])
skip = {".git", "node_modules", "corpus", "dist", "bin", ".terraform"}

index = []
for doc in sorted(root.rglob("*.md")):
    if any(p in skip for p in doc.parts):
        continue
    lines = doc.read_text(encoding="utf-8").split("\n")
    start = None
    heading = ""
    n = 0
    # Имя строится из пути, а не из имени файла: README.md в проекте не один,
    # и по одному лишь имени схемы затирали бы друг друга.
    stem = str(doc.relative_to(root)).replace("/", "_").removesuffix(".md")
    for i, line in enumerate(lines):
        if re.match(r"^#{1,6}\s", line):
            heading = line.lstrip("# ").strip()
        if line.strip() == "```mermaid":
            start = i + 1
        elif line.strip() == "```" and start is not None:
            n += 1
            name = f"{stem}-{n}"
            out.joinpath(f"{name}.mmd").write_text("\n".join(lines[start:i]), encoding="utf-8")
            index.append(f"{name}\t{doc.relative_to(root)}\t{start + 1}\t{heading}")
            start = None
# Перевод строки в конце обязателен: без него оболочка молча теряет
# последнюю строку списка, и одна схема остаётся непроверенной.
out.joinpath("index.tsv").write_text("\n".join(index) + "\n", encoding="utf-8")
print(f"извлечено схем: {len(index)}")
PY

TOTAL=0
BAD=0
while IFS=$'\t' read -r name file line heading; do
  [ -z "$name" ] && continue
  TOTAL=$((TOTAL + 1))
  if ! "$MMDC" -i "$TMP/$name.mmd" -o "$TMP/$name.svg" -q >"$TMP/$name.err" 2>&1; then
    BAD=$((BAD + 1))
    echo "СЛОМАНА  $file:$line  $heading"
    grep -iE "error|expect|parse" "$TMP/$name.err" | head -3 | sed 's/^/         /'
  fi
done < "$TMP/index.tsv"

if [ "$BAD" -gt 0 ]; then
  echo "Схем разобрано $TOTAL, сломанных $BAD."
  exit 1
fi

echo "Схем разобрано $TOTAL, сломанных нет."
exit 0
