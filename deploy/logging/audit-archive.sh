#!/usr/bin/env bash
# Складывает повёрнутые файлы журнала аудита в архив со сжатием и удаляет
# слишком старые.
#
# Почему сжатие вынесено из сервиса: сжатие это работа на целое ядро на
# десятки секунд. Внутри процесса, обслуживающего запросы, она пришлась бы на
# случайный момент и увеличила бы задержку. Здесь она идёт раз в час и её
# видно в учёте ресурсов отдельно.
#
# Отношение сжатия измерено на записях аудита: 10.5 раза, gzip с быстрым
# уровнем. Значит день работы на тысяче запросов в секунду занимает в архиве
# около 3.7 ГБ вместо 38 ГБ.
set -euo pipefail

DIR="${PII_AUDIT_DIR:-/var/log/pii-guard}"
ARCHIVE="${PII_AUDIT_ARCHIVE:-$DIR/archive}"
KEEP_DAYS="${PII_AUDIT_KEEP_DAYS:-365}"

mkdir -p "$ARCHIVE"
chmod 0750 "$ARCHIVE"

shopt -s nullglob
for file in "$DIR"/audit.log.[0-9]*; do
    case "$file" in
        *.gz) continue ;;
    esac
    stamp="$(date -u -r "$file" +%Y%m%dT%H%M%SZ 2>/dev/null || date -u +%Y%m%dT%H%M%SZ)"
    target="$ARCHIVE/audit-$stamp-$(basename "$file").gz"
    if gzip -1 -c "$file" > "$target.tmp"; then
        mv "$target.tmp" "$target"
        chmod 0640 "$target"
        rm -f "$file"
    else
        rm -f "$target.tmp"
        echo "не удалось сжать $file" >&2
    fi
done

# Срок хранения архива задаётся требованием к отчётности, а не местом на диске.
find "$ARCHIVE" -name 'audit-*.gz' -mtime "+$KEEP_DAYS" -delete
