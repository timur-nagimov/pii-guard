#!/usr/bin/env bash
# Нагрузочное тестирование сервиса маскирования персональных данных.
#
# Режимы:
#   smoke:   короткая проверка за тридцать секунд на небольшой частоте;
#   sla:     прогон по правилам проверяющей системы, тысяча запросов
#             в секунду парами, пять минут, с отчётом;
#   ceiling: поиск потолка ступенями на текстах 250 байт, 2 и 8 килобайт;
#   soak:    длительный прогон на умеренной частоте, проверка утечек;
#   k6:      независимый сценарий на k6, запускается на машине генератора.
#
# Скрипт работает и локально, и через ssh на машине генератора нагрузки
# (ключ --remote). Занятость процессоров обеих машин снимается из /proc/stat
# до и после прогона и печатается рядом с результатом: без этого нельзя
# отличить предел сервиса от предела измерителя.
#
# Примеры:
#   scripts/loadtest.sh smoke --url http://84.201.166.35
#   scripts/loadtest.sh sla --url http://84.201.166.35
#   scripts/loadtest.sh ceiling --remote --url http://10.129.0.22
#   scripts/loadtest.sh soak --remote --url http://10.129.0.22 --duration 30m
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

# Адреса стенда. Переопределяются ключами или переменными окружения.
DEFAULT_APP_HOST="84.201.166.35"
DEFAULT_LOAD_HOST="89.169.172.236"

MODE=""
URL="${URL:-http://127.0.0.1:8080}"
REMOTE=0
LOAD_HOST="${LOAD_HOST:-$DEFAULT_LOAD_HOST}"
APP_HOST="${APP_HOST:-}"
METRICS_URL="${METRICS_URL:-}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"
REMOTE_DIR="${REMOTE_DIR:-/home/ubuntu/pii-loadtest}"
OUT_ROOT="${OUT:-reports}"
RPS=""
DURATION=""
WORKERS="${WORKERS:-}"
DATASET="${DATASET:-}"
SIZES="${SIZES:-250 2048 8192}"
STEPS="${STEPS:-2000 5000 10000 15000 20000 25000 30000}"
STEP_DURATION="${STEP_DURATION:-20s}"
P99_LIMIT_MS="${P99_LIMIT_MS:-50}"
MEASURE_APP_CPU=1
SYSTEM_KEY="${SYSTEM_KEY:-}"

SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=10
  -o ControlMaster=auto -o ControlPath=/tmp/pii-loadtest-%r@%h:%p -o ControlPersist=120s)

say() { printf '%s\n' "$*"; }
note() { printf '%s\n' "$*" >&2; }
die() { printf 'ошибка: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'USAGE'

Ключи:
  --url АДРЕС          адрес сервиса, например http://84.201.166.35
  --remote             запускать генератор на машине нагрузки по ssh
  --load-host АДРЕС    машина генератора (по умолчанию 89.169.172.236)
  --app-host АДРЕС     машина сервиса для снятия занятости процессора
  --metrics-url АДРЕС  адрес ручки показателей, по умолчанию http://<app-host>/metrics
  --rps ЧИСЛО          частота запросов
  --duration СРОК      длительность прогона, например 5m
  --workers ЧИСЛО      число одновременных отправителей
  --dataset ФАЙЛ       набор текстов для sonar
  --sizes "250 2048"   размеры текстов для режима ceiling
  --steps "5000 10000" ступени частоты для режима ceiling
  --step-duration СРОК длительность одной ступени
  --p99-limit МС       порог задержки, по которому ступень считается провальной
  --system-key КЛЮЧ    значение заголовка X-System-Key
  --out КАТАЛОГ        куда складывать отчёты (по умолчанию reports)
  --no-app-cpu         не ходить по ssh на машину сервиса за занятостью процессора
USAGE
}

# parse_args разбирает ключи запуска.
parse_args() {
  [[ $# -gt 0 ]] || { usage; exit 2; }
  MODE="$1"; shift
  case "$MODE" in
    smoke | sla | ceiling | soak | k6) ;;
    -h | --help | help) usage; exit 0 ;;
    *) die "неизвестный режим: $MODE (ожидались smoke, sla, ceiling, soak, k6)" ;;
  esac
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --url) URL="$2"; shift 2 ;;
      --remote) REMOTE=1; shift ;;
      --load-host) LOAD_HOST="$2"; shift 2 ;;
      --app-host) APP_HOST="$2"; shift 2 ;;
      --metrics-url) METRICS_URL="$2"; shift 2 ;;
      --rps) RPS="$2"; shift 2 ;;
      --duration) DURATION="$2"; shift 2 ;;
      --workers) WORKERS="$2"; shift 2 ;;
      --dataset) DATASET="$2"; shift 2 ;;
      --sizes) SIZES="$2"; shift 2 ;;
      --steps) STEPS="$2"; shift 2 ;;
      --step-duration) STEP_DURATION="$2"; shift 2 ;;
      --p99-limit) P99_LIMIT_MS="$2"; shift 2 ;;
      --system-key) SYSTEM_KEY="$2"; shift 2 ;;
      --out) OUT_ROOT="$2"; shift 2 ;;
      --no-app-cpu) MEASURE_APP_CPU=0; shift ;;
      -h | --help) usage; exit 0 ;;
      *) die "неизвестный ключ: $1" ;;
    esac
  done
}

# host_of_url достаёт имя узла из адреса.
host_of_url() {
  local u="${1#*://}"
  u="${u%%/*}"
  printf '%s' "${u%%:*}"
}

# is_private_host сообщает, что по этому адресу снаружи стенда не достучаться.
is_private_host() {
  case "$1" in
    10.* | 127.* | localhost | 192.168.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) return 0 ;;
    *) return 1 ;;
  esac
}

ssh_run() { local host="$1"; shift; ssh "${SSH_OPTS[@]}" "$SSH_USER@$host" "$@"; }
scp_to() { scp "${SSH_OPTS[@]}" -q "$1" "$SSH_USER@$LOAD_HOST:$2"; }
scp_from() { scp "${SSH_OPTS[@]}" -qr "$SSH_USER@$LOAD_HOST:$1" "$2"; }

# sha_of считает контрольную сумму файла на этой машине.
sha_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# ---------- занятость процессора ----------

# cpu_line снимает первую строку /proc/stat. Пустой узел означает эту машину.
cpu_line() {
  if [[ -z "$1" ]]; then
    head -n1 /proc/stat 2>/dev/null
  else
    ssh_run "$1" 'head -n1 /proc/stat' 2>/dev/null
  fi
}

# cpu_cores возвращает число ядер узла.
cpu_cores() {
  if [[ -z "$1" ]]; then
    nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 0
  else
    ssh_run "$1" nproc 2>/dev/null || echo 0
  fi
}

# cpu_busy считает занятость процессора между двумя снимками /proc/stat.
# Занятостью считается всё время, кроме простоя и ожидания диска.
cpu_busy() {
  local before="$1" after="$2" cores="${3:-0}"
  if [[ -z "$before" || -z "$after" ]]; then
    printf 'нет данных: на машине нет /proc/stat, замер даст только запуск с --remote'
    return
  fi
  awk -v b="$before" -v a="$after" -v c="$cores" 'BEGIN {
    n = split(b, B, /[ \t]+/); split(a, A, /[ \t]+/)
    total = 0
    for (i = 2; i <= n; i++) total += A[i] - B[i]
    idle = (A[5] - B[5]) + (A[6] - B[6])
    if (total <= 0) { printf "нет данных"; exit }
    p = 100 * (total - idle) / total
    if (c > 0) printf "%.1f%% (%.1f/%d)", p, p * c / 100, c
    else printf "%.1f%%", p
  }'
}

APP_BEFORE=""; GEN_BEFORE=""; APP_CPU_TEXT=""; GEN_CPU_TEXT=""
APP_CORES=0; GEN_CORES=0; GEN_HOST=""

# cpu_setup определяет, с каких узлов снимать занятость процессора.
cpu_setup() {
  (( REMOTE )) && GEN_HOST="$LOAD_HOST"
  if (( MEASURE_APP_CPU )) && [[ -n "$APP_HOST" ]]; then
    APP_CORES="$(cpu_cores "$APP_HOST")"
  fi
  GEN_CORES="$(cpu_cores "$GEN_HOST")"
}

# cpu_start снимает показания до прогона.
cpu_start() {
  APP_BEFORE=""
  (( MEASURE_APP_CPU )) && [[ -n "$APP_HOST" ]] && APP_BEFORE="$(cpu_line "$APP_HOST")"
  GEN_BEFORE="$(cpu_line "$GEN_HOST")"
}

# cpu_stop снимает показания после прогона и считает занятость.
cpu_stop() {
  local app_after="" gen_after
  (( MEASURE_APP_CPU )) && [[ -n "$APP_HOST" ]] && app_after="$(cpu_line "$APP_HOST")"
  gen_after="$(cpu_line "$GEN_HOST")"
  APP_CPU_TEXT="$(cpu_busy "$APP_BEFORE" "$app_after" "$APP_CORES")"
  GEN_CPU_TEXT="$(cpu_busy "$GEN_BEFORE" "$gen_after" "$GEN_CORES")"
}

# cpu_report печатает занятость обеих машин.
cpu_report() {
  say "занятость процессора в формате доля процентов (занято ядер из всех)"
  say "процессор сервиса   ${APP_HOST:-не измерялся}: $APP_CPU_TEXT"
  say "процессор генератора ${GEN_HOST:-эта машина}: $GEN_CPU_TEXT"
  say "если у сервиса запас по процессору, а у генератора нет, то предел показал измеритель, а не сервис"
}

# ---------- показатели сервиса ----------

# metrics_snapshot скачивает текущие показатели сервиса.
metrics_snapshot() { curl -sk --max-time 15 "$METRICS_URL" 2>/dev/null; }

# metric_value достаёт значение показателя без меток из снимка.
metric_value() {
  awk -v name="$1" '$1 == name { v = $2 } END { if (v == "") v = 0; print v }' <<<"$2"
}

# metrics_delta печатает выбранные показатели до и после прогона.
metrics_delta() {
  local before="$1" after="$2" name unit
  for spec in \
    "pii_store_records:записей в хранилище:1" \
    "go_goroutines:горутин:1" \
    "go_memstats_heap_inuse_bytes:куча занято, МБ:1048576" \
    "go_memstats_alloc_bytes:выделено сейчас, МБ:1048576" \
    "process_resident_memory_bytes:память процесса, МБ:1048576"; do
    name="${spec%%:*}"
    local rest="${spec#*:}"
    local title="${rest%%:*}"
    unit="${rest##*:}"
    awk -v t="$title" -v u="$unit" -v b="$(metric_value "$name" "$before")" -v a="$(metric_value "$name" "$after")" \
      'BEGIN { printf "  %s: было %.2f, стало %.2f, разница %+.2f\n", t, b / u, a / u, (a - b) / u }'
  done
}

# ---------- исполняемые файлы ----------

# ensure_local_bin собирает команду для этой машины, если её нет или она старее исходников.
ensure_local_bin() {
  local name="$1" out="bin/$1"
  if [[ ! -x "$out" ]] || [[ -n "$(find "cmd/$name" -name '*.go' -newer "$out" -print -quit 2>/dev/null)" ]]; then
    note "собираю $out"
    CGO_ENABLED=0 go build -trimpath -o "$out" "./cmd/$name" >&2 || die "не удалось собрать $name"
  fi
  printf '%s' "./$out"
}

# ensure_remote_bin собирает команду под линукс и кладёт её на машину генератора.
ensure_remote_bin() {
  local name="$1" out="bin/linux-amd64/$1"
  if [[ ! -f "$out" ]] || [[ -n "$(find "cmd/$name" -name '*.go' -newer "$out" -print -quit 2>/dev/null)" ]]; then
    note "собираю $out под линукс"
    mkdir -p bin/linux-amd64
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$out" "./cmd/$name" >&2 ||
      die "не удалось собрать $name под линукс"
  fi
  push_file "$out" "$name"
  printf '%s' "$REMOTE_DIR/$name"
}

# push_file копирует файл на машину генератора, если там лежит не он.
push_file() {
  local src="$1" name="$2" local_sum remote_sum
  ssh_run "$LOAD_HOST" "mkdir -p $REMOTE_DIR" || die "нет доступа по ssh к $LOAD_HOST"
  local_sum="$(sha_of "$src")"
  remote_sum="$(ssh_run "$LOAD_HOST" "sha256sum $REMOTE_DIR/$name 2>/dev/null | cut -d' ' -f1")"
  if [[ "$local_sum" != "$remote_sum" ]]; then
    note "копирую $name на $LOAD_HOST"
    scp_to "$src" "$REMOTE_DIR/$name" || die "не удалось скопировать $name"
    ssh_run "$LOAD_HOST" "chmod +x $REMOTE_DIR/$name 2>/dev/null; true"
  fi
}

# tool возвращает путь к команде на той машине, где будет идти нагрузка.
tool() {
  if (( REMOTE )); then ensure_remote_bin "$1"; else ensure_local_bin "$1"; fi
}

# unique_dataset делает копию набора, в которой идентификаторы уникальны для
# этого прогона. Без этого повторный прогон попадает на записи предыдущего:
# сервис хранит соответствие идентификатора и текста, и старая запись с тем же
# идентификатором портит измерение обратного шага.
unique_dataset() {
  local src="$1" dst="$OUTDIR/dataset.jsonl"
  if ! command -v python3 >/dev/null 2>&1; then
    note "python3 не найден, идентификаторы набора остались прежними"
    printf '%s' "$src"
    return
  fi
  if ! python3 scripts/loadtest-ids.py "$src" "$dst" "$RUNID" 2>/dev/null; then
    note "набор $src не переписан под прогон, беру его как есть"
    printf '%s' "$src"
    return
  fi
  printf '%s' "$dst"
}

# ensure_dataset проверяет набор текстов, при необходимости собирает его,
# делает копию с уникальными идентификаторами и кладёт её туда, откуда пойдёт
# нагрузка.
ensure_dataset() {
  local path="$1"
  if [[ ! -f "$path" ]]; then
    note "набор $path не найден, собираю генератором"
    mkdir -p "$(dirname "$path")"
    go run ./cmd/gen -out "$path" -n 8000 -seed 42 >&2 || die "не удалось собрать набор $path"
  fi
  path="$(unique_dataset "$path")"
  if (( REMOTE )); then
    push_file "$path" "$(basename "$path")"
    printf '%s/%s' "$REMOTE_DIR" "$(basename "$path")"
  else
    printf '%s' "$path"
  fi
}

# target_run выполняет команду на машине генератора: локально либо по ssh.
target_run() {
  if (( REMOTE )); then
    ssh_run "$LOAD_HOST" "cd $REMOTE_DIR && $*"
  else
    eval "$*"
  fi
}

# ---------- разбор вывода генератора ----------

field_rate() { sed -n 's/.*достигнуто \([0-9.]*\) запросов.*/\1/p' <<<"$1" | tail -1; }
field_ok() { sed -n 's/^успешно \([0-9]*\),.*/\1/p' <<<"$1" | tail -1; }
field_throttled() { sed -n 's/.*с просьбой повторить \([0-9]*\),.*/\1/p' <<<"$1" | tail -1; }
field_failed() { sed -n 's/.*ошибок \([0-9]*\)$/\1/p' <<<"$1" | tail -1; }
field_quantile() { sed -n "s/.*доля $2 \([0-9.]*\) мс.*/\1/p" <<<"$1" | tail -1; }

# ---------- режимы ----------

# warn_if_rate_missed сообщает о недоборе заданной частоты. Недобор сам по
# себе не приговор сервису: чаще всего его причина в задержке сети либо в
# нехватке отправителей, и тогда числа задержки описывают канал, а не сервис.
warn_if_rate_missed() {
  local target="$1" achieved
  achieved="$(sed -n 's/.*"achieved_rps": \([0-9.]*\).*/\1/p' "$OUTDIR/report.json" 2>/dev/null | head -1)"
  [[ -z "$achieved" ]] && return 0
  awk -v t="$target" -v a="$achieved" 'BEGIN { exit !(a < 0.9 * t) }' || return 0
  say "внимание: набрано $achieved из $target запросов в секунду."
  say "смотрите занятость процессоров ниже: если оба свободны, упёрлись в задержку сети"
  say "или в число отправителей, поднимите --workers либо запускайте с --remote"
}

OUTDIR=""
RUNID=""

# prepare_out создаёт каталог отчёта прогона.
prepare_out() {
  RUNID="$(date +%Y%m%d-%H%M%S)"
  OUTDIR="$OUT_ROOT/$MODE-$RUNID"
  mkdir -p "$OUTDIR" || die "не удалось создать каталог отчёта $OUTDIR"
}

# run_sonar запускает имитатор проверяющей системы и забирает отчёт.
run_sonar() {
  local rps="$1" duration="$2" dataset="$3" workers="$4" extra="${5:-}"
  local bin remote_out="report-$(date +%s)" out_flag="$OUTDIR"
  bin="$(tool sonar)" || exit 1
  (( REMOTE )) && out_flag="$remote_out"
  cpu_start
  target_run "$bin -url $URL -dataset $dataset -rps $rps -duration $duration -workers $workers -out $out_flag $extra" |
    tee "$OUTDIR/output.txt"
  local code=${PIPESTATUS[0]}
  cpu_stop
  if (( REMOTE )); then
    scp_from "$REMOTE_DIR/$remote_out/*" "$OUTDIR/" 2>/dev/null
    ssh_run "$LOAD_HOST" "rm -rf $REMOTE_DIR/$remote_out"
  fi
  return "$code"
}

# run_loadgen запускает чистый генератор нагрузки. Результат кладётся
# в LOADGEN_OUT, а не печатается: вызов через $( ) увёл бы замеры занятости
# процессора в подоболочку и потерял их.
LOADGEN_OUT=""
run_loadgen() {
  local rps="$1" duration="$2" payload="$3" workers="$4" bin
  bin="$(tool loadgen)" || exit 1
  cpu_start
  LOADGEN_OUT="$(target_run "$bin -url $URL -rps $rps -duration $duration -workers $workers -payload $payload")"
  cpu_stop
}

mode_smoke() {
  local rps="${RPS:-100}" duration="${DURATION:-30s}" dataset
  dataset="$(ensure_dataset "${DATASET:-testdata/sample.jsonl}")" || exit 1
  say "== короткая проверка: $URL, $rps запросов в секунду, $duration =="
  run_sonar "$rps" "$duration" "$dataset" "${WORKERS:-64}"
  local code=$?
  say ""
  warn_if_rate_missed "$rps"
  cpu_report
  say "отчёт: $OUTDIR/report.md"
  if (( code != 0 )); then
    say "ПРОВАЛ: прогон завершился с ошибкой, смотрите вывод выше"
    return 1
  fi
  say "готово: если доля точного восстановления сто процентов и невалидных ответов нет, сервис здоров"
}

mode_sla() {
  local rps="${RPS:-1000}" duration="${DURATION:-5m}" dataset
  dataset="$(ensure_dataset "${DATASET:-corpus/dataset.jsonl}")" || exit 1
  say "== прогон по правилам проверяющей системы: $URL, $rps запросов в секунду парами, $duration =="
  say "запросы идут парами: прямой и обратный, поэтому пар в секунду вдвое меньше"
  run_sonar "$rps" "$duration" "$dataset" "${WORKERS:-64}" "-dup-after-success -demask-retry -big-payload 1048576"
  local code=$?
  say ""
  cpu_report
  say "отчёт: $OUTDIR/report.md и $OUTDIR/report.json"
  return "$code"
}

# ceiling_row печатает строку таблицы по одной ступени.
ceiling_row() {
  local size="$1" target="$2" out="$3"
  printf '| %6d | %7d | %10s | %6s | %6s | %6s | %7s | %6s | %-26s | %-26s |\n' \
    "$size" "$target" "$(field_rate "$out")" "$(field_quantile "$out" 50)" \
    "$(field_quantile "$out" 95)" "$(field_quantile "$out" 99)" \
    "$(field_throttled "$out")" "$(field_failed "$out")" "$APP_CPU_TEXT" "$GEN_CPU_TEXT"
}

# ceiling_reason называет причину провала ступени и пустую строку, если
# ступень засчитана.
ceiling_reason() {
  local target="$1" out="$2"
  awk -v t="$target" -v r="$(field_rate "$out")" -v p="$(field_quantile "$out" 99)" \
    -v th="$(field_throttled "$out")" -v f="$(field_failed "$out")" -v lim="$P99_LIMIT_MS" '
    BEGIN {
      if (r < 0.92 * t) printf "частота не набрана: %s из %s", r, t
      else if (f + 0 > 0) printf "ошибки запросов: %s", f
      else if (th + 0 > 0) printf "отказы с просьбой повторить: %s", th
      else if (p + 0 > lim) printf "доля 99 задержки %s мс выше порога %s мс", p, lim
    }'
}

# ceiling_for_size проходит ступени частоты для одного размера текста.
ceiling_for_size() {
  local size="$1" target out reason last=0 failed=0
  for target in $STEPS; do
    run_loadgen "$target" "$STEP_DURATION" "$size" "${WORKERS:-64}"
    out="$LOADGEN_OUT"
    printf '%s\n' "--- размер $size байт, цель $target ---" "$out" \
      "процессор сервиса: $APP_CPU_TEXT, процессор генератора: $GEN_CPU_TEXT" >>"$OUTDIR/output.txt"
    ceiling_row "$size" "$target" "$out"
    reason="$(ceiling_reason "$target" "$out")"
    if [[ -n "$reason" ]]; then
      say "ступень $target провалена: $reason"
      failed=1
      break
    fi
    last="$(field_rate "$out")"
  done
  if [[ "$last" == 0 ]]; then
    say "потолок на текстах $size байт: ниже первой ступени, опустите --steps"
  elif (( failed == 0 )); then
    say "на текстах $size байт пройдены все ступени, потолок выше ${last%.*} запросов в секунду: поднимите --steps"
  else
    say "потолок на текстах $size байт: около ${last%.*} запросов в секунду"
  fi
  say ""
}

mode_ceiling() {
  say "== поиск потолка: $URL, ступени $STEPS, по $STEP_DURATION на ступень =="
  say "| размер | цель    | достигнуто | доля50 | доля95 | доля99 | отказов | ошибок | процессор сервиса          | процессор генератора       |"
  say "|--------|---------|------------|--------|--------|--------|---------|--------|----------------------------|----------------------------|"
  local size
  for size in $SIZES; do
    ceiling_for_size "$size"
  done
  say "подробный вывод: $OUTDIR/output.txt"
  say "ступень засчитана, если достигнуто не меньше 92 процентов цели, доля 99 задержки ниже $P99_LIMIT_MS мс и нет отказов"
}

mode_soak() {
  local rps="${RPS:-500}" duration="${DURATION:-30m}" size="${SIZES%% *}" before after out
  say "== длительный прогон: $URL, $rps запросов в секунду, $duration, тексты по $size байт =="
  before="$(metrics_snapshot)"
  [[ -z "$before" ]] && say "внимание: показатели с $METRICS_URL не снялись, утечки не проверить"
  run_loadgen "$rps" "$duration" "$size" "${WORKERS:-64}"
  out="$LOADGEN_OUT"
  printf '%s\n' "$out" | tee "$OUTDIR/output.txt"
  after="$(metrics_snapshot)"
  say ""
  cpu_report
  say ""
  if [[ -n "$before" && -n "$after" ]]; then
    say "показатели сервиса до и после прогона:"
    metrics_delta "$before" "$after" | tee "$OUTDIR/metrics-delta.txt"
    printf '%s' "$before" >"$OUTDIR/metrics-before.txt"
    printf '%s' "$after" >"$OUTDIR/metrics-after.txt"
    say "записи хранилища живут ограниченное время, поэтому их число после прогона должно перестать расти,"
    say "а память процесса вернуться к прежнему уровню за время жизни записей"
  fi
}

mode_k6() {
  (( REMOTE )) || die "сценарий k6 установлен на машине генератора, добавьте --remote"
  local rps="${RPS:-500}" duration="${DURATION:-1m}" size="${SIZES%% *}"
  push_file deploy/k6/pii-guard.js pii-guard.js
  say "== сценарий k6: $URL, $rps пар в секунду, $duration =="
  cpu_start
  ssh_run "$LOAD_HOST" "cd $REMOTE_DIR && BASE_URL=$URL RPS=$rps DURATION=$duration PAYLOAD_SIZE=$size \
    SYSTEM_KEY=$SYSTEM_KEY P99_MS=$P99_LIMIT_MS ${K6_EXTRA_ENV:-} k6 run ${K6_OUT:+-o $K6_OUT} pii-guard.js" |
    tee "$OUTDIR/output.txt"
  local code=${PIPESTATUS[0]}
  cpu_stop
  say ""
  cpu_report
  return "$code"
}

main() {
  parse_args "$@"
  command -v curl >/dev/null 2>&1 || die "нужна команда curl"
  command -v go >/dev/null 2>&1 || die "нужен компилятор go: скрипт собирает генераторы сам"
  URL="${URL%/}"
  local url_host
  url_host="$(host_of_url "$URL")"
  if [[ -z "$APP_HOST" ]]; then
    if is_private_host "$url_host"; then APP_HOST="$DEFAULT_APP_HOST"; else APP_HOST="$url_host"; fi
  fi
  [[ -n "$METRICS_URL" ]] || METRICS_URL="http://$APP_HOST/metrics"
  prepare_out
  cpu_setup
  say "режим $MODE, сервис $URL, отчёты в $OUTDIR"
  say ""
  case "$MODE" in
    smoke) mode_smoke ;;
    sla) mode_sla ;;
    ceiling) mode_ceiling ;;
    soak) mode_soak ;;
    k6) mode_k6 ;;
  esac
}

main "$@"
