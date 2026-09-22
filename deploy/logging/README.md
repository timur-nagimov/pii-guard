# Сбор журнала

Файлы этой папки ставят сбор журнала на машину со стендом. Разбор вариантов,
стандарт полей и расчёт объёма лежат в `docs/LOGGING.md`.

| Файл | Куда ставится | Что делает |
| --- | --- | --- |
| `journald.conf` | `/etc/systemd/journald.conf.d/pii-guard.conf` | пределы места и срока хранения, выключение собственного ограничителя частоты journald |
| `pii-guard-logging.conf` | `/etc/systemd/system/pii-guard.service.d/logging.conf` | уровень и формат журнала, каталог и параметры журнала аудита |
| `audit-archive.sh` | `/opt/pii-guard/deploy/logging/` | сжатие повёрнутых файлов аудита в архив, удаление слишком старых |
| `audit-archive.service`, `audit-archive.timer` | `/etc/systemd/system/` | запуск архивации раз в час |
| `install.sh` | — | ставит всё перечисленное и перезапускает службы |
| `loki/` | — | необязательный слой: единое окно над журналом в Grafana |

## Установка

```bash
sudo /opt/pii-guard/deploy/logging/install.sh
```

Скрипт идемпотентен: повторный запуск доводит настройку до нужного состояния.

Пакет журнала подключается к сервису отдельной правкой точек входа, она
описана в разделе «Подключение к точкам входа» документа `docs/LOGGING.md` и
на момент написания не применена. До неё работают пределы journald и
архивация аудита, а ручка управления уровнем и показатели журнала появятся
вместе с ней.

## Проверка

```bash
# записи идут и разбираются как JSON
journalctl -u pii-guard -n 20 -o cat | jq .

# в журнале нет значений персональных данных: счётчик срабатываний защиты
curl -s localhost:8080/metrics | grep pii_log_redactions_total

# журнал аудита пишется в отдельный файл
sudo ls -l /var/log/pii-guard/

# архивация настроена
systemctl list-timers audit-archive.timer
```

## Полезные выборки

```bash
# всё по одному запросу, включая обращение к языковой модели
journalctl -u pii-guard -o cat | jq -c 'select(.request_id == "ИДЕНТИФИКАТОР")'

# отказы с просьбой повторить за последний час
journalctl -u pii-guard --since -1h -o cat | jq -c 'select(.status == 429)'

# что и когда глушил журнал
journalctl -u pii-guard -o cat | jq -c 'select(.event == "log_suppressed")'

# аудит по одной системе за сутки
sudo jq -c 'select(.system == "crm")' /var/log/pii-guard/audit.log
```

## Подробность журнала без перезапуска

```bash
# поднять на пять минут, вернётся само
curl -XPOST 'localhost:8080/admin/loglevel?level=debug&ttl=5m'

# посмотреть текущий уровень
curl -s localhost:8080/admin/loglevel

# вернуть вручную
curl -XPOST 'localhost:8080/admin/loglevel?level=info'
```

Ручка меняет поведение сервиса и наружу выставляться не должна: либо
отдельный слушатель на петлевом адресе, либо проверка ключа, либо запрет на
балансировщике.

## Необязательный слой Loki

Нужен, только если требуется искать по журналу в том же окне Grafana, где
лежит панель показателей. Цена: два контейнера и около 400 мегабайт памяти.

```bash
cp deploy/logging/loki/datasource.yml deploy/grafana/datasources/loki.yml
docker compose -f docker-compose.yml \
  -f deploy/logging/loki/docker-compose.logging.yml up -d loki promtail
```

Без этого слоя журнал читается через `journalctl`, а оповещения строятся по
показателям Prometheus, которые уже собираются.
