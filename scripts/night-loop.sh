#!/usr/bin/env bash
# Непрерывный прогон улучшений: работает, пока не остановят.
#
# Чем отличается от scripts/night.sh. Тот берёт готовый список задач и, когда
# он кончается, завершается. Это оказалось ошибкой: очередь опустела в первом
# часу ночи, и дальше машина простаивала до утра.
#
# Здесь работы не кончаются. После каждого круга список задач собирается
# ЗАНОВО из текущего состояния: берутся самые слабые типы по свежему замеру,
# и на каждый заводится задача. Слабые места меняются по мере починки, значит
# и задачи каждый круг другие.
#
# Остановить:  touch <каталог прогона>/STOP   либо  Ctrl+C
#
# Запуск:
#   bash scripts/night-loop.sh                 бесконечно
#   bash scripts/night-loop.sh --until 23:00   до указанного времени
#   bash scripts/night-loop.sh --jobs 3        сколько задач разом

set -o pipefail

cd "$(dirname "$0")/.." || exit 2
ROOT="$PWD"
NIGHT="${NIGHT_DIR:-$ROOT/../night}"
MODEL="${NIGHT_MODEL:-alfagen/deepseek-ai/DeepSeek-V4-Flash-0731}"
JOBS="${NIGHT_JOBS:-3}"
UNTIL=""
ROUND=0

while [ $# -gt 0 ]; do
  case "$1" in
    --jobs)  JOBS="$2"; shift ;;
    --until) UNTIL="$2"; shift ;;
    --model) MODEL="$2"; shift ;;
    *) echo "неизвестный ключ: $1"; exit 2 ;;
  esac
  shift
done

mkdir -p "$NIGHT"/{work,passed,parked,logs,rounds}
STOP="$NIGHT/STOP"
rm -f "$STOP"

say() { printf '\n\033[1m[%s] %s\033[0m\n' "$(date '+%H:%M:%S')" "$1"; }

# Пора останавливаться?
should_stop() {
  if [ -f "$STOP" ]; then
    say "найден файл STOP, останавливаюсь"
    return 0
  fi
  if [ -n "$UNTIL" ] && [ "$(date '+%H:%M')" \> "$UNTIL" ]; then
    say "настало $UNTIL, останавливаюсь"
    return 0
  fi
  return 1
}

# Самые слабые типы по свежему замеру. Именно отсюда берутся задачи круга,
# поэтому список меняется сам по мере того, как слабые места чинятся.
weakest() {
  local n="${1:-3}"
  local corpus="${GATE_CORPUS:-corpus/dataset.jsonl}"
  [ -f "$corpus" ] || go run ./cmd/gen -out "$corpus" -n 8000 -seed 42 >/dev/null 2>&1
  go run ./cmd/score -dataset "$corpus" 2>/dev/null \
    | grep -E '^[A-Z_]+ ' \
    | sort -k2 -n \
    | head -"$n" \
    | awk '{print $1 ":" $2}'
}

# Задание для одного типа. Текст один и тот же, меняется тип и его балл:
# узкая задача с понятным критерием работает и на слабой модели, широкая
# не работает и на сильной.
task_prompt() {
  local type="$1" score="$2"
  cat <<EOF
Задача: поднять качество распознавания типа $type.

Сейчас его доля изменённого равна $score, и это одно из самых слабых мест
решения. Файлы во владении: internal/pii/ (детекторы, контекстные правила,
словари), cmd/gen (генератор набора, если не хватает примеров).

КАК РАБОТАТЬ. Сначала посмотреть на промахи, потом править, потом замерить:

  go run ./cmd/score -dataset corpus/dataset.jsonl -type $type -examples 30

Разбери тридцать промахов и отнеси каждый к разряду: чинится правилом,
чинится только словарём, спорная разметка, неразрешимо без внешнего знания.
Чини то, что чинится ПРАВИЛОМ: оно обобщается. Словарь растить в последнюю
очередь, он не переносится на новые данные.

ЖЁСТКОЕ ОГРАНИЧЕНИЕ. Отрицательные срезы neg_* обязаны остаться строго
нулевыми. Правка, которая поднимает свой тип ценой ложных срабатываний,
считается неверной и откатывается, а не дорабатывается. Ложное срабатывание
портит текст у всех пользователей, а пропуск это только недобор полноты.

ГОТОВО, когда bash scripts/gate.sh возвращает ноль. Это семнадцать проверок
одной командой, включая сравнение качества с базовой линией и поиск
персональных данных в журнале. Базовую линию scripts/baseline.json трогать
НЕЛЬЗЯ: линейка, которую двигает тот же, кто правит код, ничего не измеряет.

В отчёте: таблица «было, стало, чем починено» и доли разрядов промахов.
EOF
}

say "прогон пошёл. Круги бесконечны, задачи собираются заново каждый круг."
[ -n "$UNTIL" ] && echo "  остановлюсь в $UNTIL"
echo "  остановить руками: touch $STOP"
echo "  следить: tail -f $NIGHT/logs/*.log"

while ! should_stop; do
  ROUND=$((ROUND + 1))
  say "круг $ROUND: смотрю, где сейчас слабее всего"

  # mapfile в bash 3.2, который стоит на macOS, отсутствует: читаем циклом.
  TARGETS=()
  while IFS= read -r line; do
    [ -n "$line" ] && TARGETS+=("$line")
  done < <(weakest "$JOBS")
  if [ "${#TARGETS[@]}" -eq 0 ]; then
    say "замер не отработал, жду минуту и пробую снова"
    sleep 60
    continue
  fi

  printf '  цели круга:'
  for t in "${TARGETS[@]}"; do printf ' %s' "${t%%:*}"; done
  printf '\n'

  PIDS=""
  for entry in "${TARGETS[@]}"; do
    type="${entry%%:*}"
    score="${entry##*:}"
    name="$(printf '%s' "$type" | tr 'A-Z_' 'a-z-')-r$ROUND"
    dir="$NIGHT/work/$name"
    branch="night/$name"
    log="$NIGHT/logs/$name.log"

    (
      {
        echo "=== $name: начало $(date '+%H:%M:%S') ==="
        git -C "$ROOT" worktree add -q -b "$branch" "$dir" HEAD 2>&1 || exit 1

        mkdir -p "$dir/corpus"
        for f in "$ROOT"/corpus/*; do
          base="$(basename "$f")"
          [ "$base" = "dataset.jsonl" ] && continue
          ln -sf "$f" "$dir/corpus/$base" 2>/dev/null
        done
        cp "$ROOT/corpus/dataset.jsonl" "$dir/corpus/dataset.jsonl" 2>/dev/null

        task_prompt "$type" "$score" \
          | opencode run --auto --dir "$dir" --model "$MODEL" --title "$name" 2>&1 | tail -30

        # Работу фиксируем сами: без этого сливать утром нечего.
        ( cd "$dir" && git add -A -- . ':!corpus' \
          && (git diff --cached --quiet \
              || git -c user.name=night -c user.email=night@local commit -q -m "Круг $ROUND: $type") )

        echo "=== $name: ворота $(date '+%H:%M:%S') ==="
        ( cd "$dir" && bash scripts/gate.sh )
        if [ $? = 0 ]; then
          touch "$NIGHT/passed/$name.ok"
          echo "=== $name: ВОРОТА ОТКРЫТЫ ==="
        else
          echo "закрыты" > "$NIGHT/parked/$name.fail"
          echo "=== $name: ворота закрыты ==="
        fi
        echo "=== $name: конец $(date '+%H:%M:%S') ==="
      } > "$log" 2>&1
    ) &
    PIDS="$PIDS $!"
    printf '  пошла задача %s (%s = %s)\n' "$name" "$type" "$score"
  done

  for p in $PIDS; do wait "$p" 2>/dev/null; done
  say "круг $ROUND закончен"

  # Ветки круга, прошедшие ворота, вливаются в рабочую ветку сразу: иначе
  # следующий круг мерил бы старое состояние и чинил бы уже починенное.
  for entry in "${TARGETS[@]}"; do
    type="${entry%%:*}"
    name="$(printf '%s' "$type" | tr 'A-Z_' 'a-z-')-r$ROUND"
    if [ -f "$NIGHT/passed/$name.ok" ]; then
      if git -C "$ROOT" merge --no-ff --no-edit "night/$name" >/dev/null 2>&1; then
        printf '  влито: %s\n' "$name"
      else
        git -C "$ROOT" merge --abort 2>/dev/null
        printf '  конфликт при слиянии %s, ветка оставлена на разбор\n' "$name"
      fi
    fi
    git -C "$ROOT" worktree remove --force "$NIGHT/work/$name" 2>/dev/null
  done

  say "состояние после круга $ROUND"
  go run ./cmd/score -dataset "${GATE_CORPUS:-corpus/dataset.jsonl}" 2>/dev/null \
    | grep -E '^[A-Z_]+ ' | sort -k2 -n | head -5 | awk '{printf "    %-18s %s\n",$1,$2}' \
    | tee "$NIGHT/rounds/round-$ROUND.txt"
done

say "прогон остановлен, кругов пройдено: $ROUND"
