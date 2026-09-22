#!/usr/bin/env bash
# Поднимает наблюдение на уже работающей машине, не пересоздавая её.
#
# Зачем нужен. Облачная инициализация выполняется один раз, при создании
# машины. Машина сервиса создана до того, как модуль научился разворачивать
# Prometheus и Grafana, поэтому на ней наблюдения нет. Скрипт кладёт на машину
# ровно те же файлы, что положила бы облачная инициализация, и поднимает
# контейнеры. После пересоздания машины он не нужен.
#
# Использование:
#   ./push-monitoring.sh 84.201.166.35 [порт сервиса, по умолчанию 80]
set -euo pipefail

HOST=${1:?укажите адрес машины}
APP_PORT=${2:-80}
GRAFANA_PORT=${GRAFANA_PORT:-3000}
RETENTION=${RETENTION:-72h}
SSH_KEY=${SSH_KEY:-$HOME/.ssh/id_ed25519}
SSH_USER=${SSH_USER:-ubuntu}

HERE=$(cd "$(dirname "$0")" && pwd)
SRC="$HERE/cloud-init/monitoring"
REPO_DASH="$HERE/../grafana/dashboards"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/grafana/datasources" "$WORK/grafana/dashboards"
cp "$SRC/grafana/datasources/prometheus.yml" "$WORK/grafana/datasources/"

# Панели берутся оттуда же, откуда их берёт модуль: сначала репозиторий,
# иначе встроенные в модуль.
if compgen -G "$REPO_DASH/*.json" > /dev/null; then
  cp "$REPO_DASH"/*.json "$WORK/grafana/dashboards/"
  cp "${REPO_DASH}/dashboards.yml" "$WORK/grafana/dashboards/" 2>/dev/null || \
    cp "$SRC/grafana/dashboards/dashboards.yml" "$WORK/grafana/dashboards/"
else
  cp "$SRC/dashboards"/*.json "$WORK/grafana/dashboards/"
  cp "$SRC/grafana/dashboards/dashboards.yml" "$WORK/grafana/dashboards/"
fi

# Подстановки те же, что делает templatefile в модуле.
sed -e "s|\${grafana_port}|$GRAFANA_PORT|g" -e "s|\${retention}|$RETENTION|g" \
  "$SRC/compose.yml.tftpl" > "$WORK/docker-compose.yml"
sed -e "s|\${app_targets}|\"127.0.0.1:$APP_PORT\"|g" \
  "$SRC/prometheus.yml.tftpl" > "$WORK/prometheus.yml"

tar -czf "$WORK/monitoring.tgz" -C "$WORK" docker-compose.yml prometheus.yml grafana
scp -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new "$WORK/monitoring.tgz" "$SSH_USER@$HOST:/tmp/monitoring.tgz"
ssh -i "$SSH_KEY" "$SSH_USER@$HOST" '
  set -e
  sudo mkdir -p /opt/pii-guard/monitoring
  sudo tar -xzf /tmp/monitoring.tgz -C /opt/pii-guard/monitoring
  rm -f /tmp/monitoring.tgz
  sudo docker compose -f /opt/pii-guard/monitoring/docker-compose.yml up -d
  sleep 5
  sudo docker compose -f /opt/pii-guard/monitoring/docker-compose.yml ps
'
echo "панель: http://$HOST:$GRAFANA_PORT"
