#!/usr/bin/env bash
# Ставит сбор журнала на машину со стендом. Идемпотентен: повторный запуск
# доводит настройку до нужного состояния и не ломает уже настроенное.
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

install -D -m 0644 "$SRC/journald.conf" /etc/systemd/journald.conf.d/pii-guard.conf
install -D -m 0644 "$SRC/pii-guard-logging.conf" /etc/systemd/system/pii-guard.service.d/logging.conf
install -D -m 0644 "$SRC/audit-archive.service" /etc/systemd/system/audit-archive.service
install -D -m 0644 "$SRC/audit-archive.timer" /etc/systemd/system/audit-archive.timer
install -D -m 0755 "$SRC/audit-archive.sh" /opt/pii-guard/deploy/logging/audit-archive.sh

systemctl restart systemd-journald
systemctl daemon-reload
systemctl restart pii-guard
systemctl enable --now audit-archive.timer

echo "готово. проверка:"
echo "  journalctl -u pii-guard -n 20 -o json-pretty"
echo "  ls -l /var/log/pii-guard/"
echo "  systemctl list-timers audit-archive.timer"
