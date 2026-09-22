#!/usr/bin/env bash
# Выкладка в закрытый репозиторий GitHub. Запускать из корня проекта.
set -euo pipefail
NAME="${1:-pii-guard}"
FRIEND="${2:-}"

command -v gh >/dev/null || { echo "нет клиента gh"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "сначала выполните: gh auth login"; exit 1; }

echo "=== последняя проверка перед отправкой ==="
git status --porcelain | grep . && { echo "есть незафиксированные изменения, зафиксируйте их"; exit 1; } || echo "  рабочее дерево чистое"
for f in .env .keys-plain.txt; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 && { echo "  ОСТАНОВКА: $f отслеживается"; exit 1; }
done
echo "  файлы с ключами не отслеживаются"
git ls-files 'testdata/*.jsonl' | xargs grep -l '"source":"hf_ru_pii"\|"source":"reviews"\|"source":"wikipedia"' 2>/dev/null \
  && { echo "  ОСТАНОВКА: в пробах чужие данные"; exit 1; } || echo "  чужих данных в пробах нет"

echo "=== создаю закрытый репозиторий ==="
gh repo create "$NAME" --private --source=. --remote=origin --push
URL=$(gh repo view "$NAME" --json url -q .url)
echo "готово: $URL"

if [[ -n "$FRIEND" ]]; then
  gh api -X PUT "repos/:owner/$NAME/collaborators/$FRIEND" -f permission=push >/dev/null
  echo "приглашение отправлено: $FRIEND получил доступ на запись"
else
  echo "чтобы добавить соавтора: gh api -X PUT repos/:owner/$NAME/collaborators/<логин> -f permission=push"
fi
