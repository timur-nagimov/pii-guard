// Наблюдение за стендом.
//
// Выбран вариант «Prometheus и Grafana на машине сервиса облачной
// инициализацией», а не отдельная машина наблюдения. Причины три.
// Первая: машина сервиса на штатной нагрузке занята на десятую часть, три
// контейнера наблюдения на фоне двадцати четырёх ядер не искажают замер, а
// отдельная машина это плюс четыре рубля в час за то же самое.
// Вторая: панель оказывается на том же внешнем адресе, что и сервис, порт
// 3000 уже открыт и уже записан в документах для жюри, лишний адрес не нужен.
// Третья: меньше точек отказа. Наблюдение, которое живёт на другой машине,
// ломается ровно тогда, когда между машинами что-то не так с сетью, то есть в
// самый нужный момент.
//
// У варианта есть цена: при пересоздании машины история показателей теряется,
// и при масштабировании панель не может жить на машине из группы. Поэтому
// отдельная машина наблюдения тоже описана, включается enable_monitor_vm и
// обязательна в режиме масштабирования.

locals {
  monitoring_dir = "${path.module}/cloud-init/monitoring"

  // Панели берутся из репозитория, если они там есть: их ведёт команда.
  // Если каталог пуст, разворачивается встроенная панель из этого модуля,
  // чтобы стенд с нуля поднимался сразу с готовыми показателями.
  repo_dashboards_dir = "${path.module}/../grafana/dashboards"
  repo_dashboards     = try(fileset(local.repo_dashboards_dir, "*.json"), toset([]))
  builtin_dashboards  = try(fileset("${local.monitoring_dir}/dashboards", "*.json"), toset([]))

  dashboards_b64 = length(local.repo_dashboards) > 0 ? {
    for name in local.repo_dashboards : name => filebase64("${local.repo_dashboards_dir}/${name}")
    } : {
    for name in local.builtin_dashboards : name => filebase64("${local.monitoring_dir}/dashboards/${name}")
  }

  // Настройка провижининга панелей берётся оттуда же, откуда сами панели:
  // иначе путь в ней разойдётся с тем, куда панели положены.
  dashboards_provider_b64 = length(local.repo_dashboards) > 0 && fileexists("${local.repo_dashboards_dir}/dashboards.yml") ? filebase64("${local.repo_dashboards_dir}/dashboards.yml") : filebase64("${local.monitoring_dir}/grafana/dashboards/dashboards.yml")

  // Наблюдение на машине сервиса и на отдельной машине не нужны одновременно.
  monitoring_on_app = var.enable_monitoring_on_app && !var.enable_monitor_vm

  monitoring_compose = templatefile("${local.monitoring_dir}/compose.yml.tftpl", {
    grafana_port = var.grafana_port
    retention    = var.metrics_retention
  })

  // Цели сбора. На машине сервиса это она сама, на отдельной машине
  // наблюдения это машина сервиса плюс всё, что добавлено переменной: туда
  // вписываются адреса копий из группы машин.
  monitoring_targets_app     = ["127.0.0.1:${var.app_http_port}"]
  monitoring_targets_monitor = concat(["${yandex_compute_instance.app.network_interface.0.ip_address}:${var.app_http_port}"], var.monitor_extra_targets)

  // Общая часть файлов наблюдения: она одинакова на любой машине.
  monitoring_files_common = merge(
    {
      "/opt/pii-guard/monitoring/docker-compose.yml"                 = base64encode(local.monitoring_compose)
      "/opt/pii-guard/monitoring/grafana/datasources/prometheus.yml" = filebase64("${local.monitoring_dir}/grafana/datasources/prometheus.yml")
      "/opt/pii-guard/monitoring/grafana/dashboards/dashboards.yml"  = local.dashboards_provider_b64
    },
    { for name, content in local.dashboards_b64 : "/opt/pii-guard/monitoring/grafana/dashboards/${name}" => content }
  )

  monitoring_files_app = merge(local.monitoring_files_common, {
    "/opt/pii-guard/monitoring/prometheus.yml" = base64encode(templatefile("${local.monitoring_dir}/prometheus.yml.tftpl", {
      app_targets = join(", ", [for t in local.monitoring_targets_app : "\"${t}\""])
    }))
  })

  monitoring_files_monitor = merge(local.monitoring_files_common, {
    "/opt/pii-guard/monitoring/prometheus.yml" = base64encode(templatefile("${local.monitoring_dir}/prometheus.yml.tftpl", {
      app_targets = join(", ", [for t in local.monitoring_targets_monitor : "\"${t}\""])
    }))
  })

  // Размер полезной нагрузки наблюдения. Облачная инициализация целиком не
  // должна превышать 256 КБ, и растут в ней именно панели.
  monitoring_payload_bytes = length(local.monitoring_files_app) == 0 ? 0 : sum([for content in values(local.monitoring_files_app) : length(content)])
}

// Отдельная машина наблюдения. Нужна в режиме масштабирования: копии сервиса
// в группе появляются и исчезают, а история показателей и панель для жюри
// должны это пережить.
resource "yandex_compute_instance" "monitor" {
  count = var.enable_monitor_vm ? 1 : 0

  name        = "pii-guard-monitor"
  hostname    = "pii-guard-monitor"
  platform_id = "standard-v3"
  zone        = var.zone

  resources {
    cores         = var.monitor_cores
    memory        = var.monitor_memory_gb
    core_fraction = 100
  }

  boot_disk {
    initialize_params {
      name     = "pii-guard-monitor-boot"
      image_id = data.yandex_compute_image.ubuntu.id
      type     = "network-ssd"
      size     = 40
    }
  }

  network_interface {
    subnet_id          = local.subnet_id
    nat                = true
    security_group_ids = [yandex_vpc_security_group.stand.id]
  }

  metadata = {
    ssh-keys = "ubuntu:${var.ssh_public_key}"
    user-data = templatefile("${path.module}/cloud-init/monitor.yaml.tftpl", {
      monitoring_files = local.monitoring_files_monitor
    })
  }
}

// --- Доставка показателей сервиса в Yandex Monitoring ---
//
// Группа машин из autoscale.tf умеет расти по показателю самого сервиса
// pii_capacity_used_ratio, но читает она его не с ручки /metrics, а из Yandex
// Monitoring. Переносчика между ними на стенде нет, поэтому сигнальный вариант
// роста включить нельзя: предусловие в autoscale.tf требует
// autoscale_metrics_delivered = true, и поставить этот флаг сейчас означало бы
// соврать модулю.
//
// Чего не хватает. Проверено по коду модуля, а не по памяти:
//
//   1. Переносчика нет. Yandex Unified Agent умеет забирать показатели в
//      формате Prometheus и складывать их в Monitoring, но в облачной
//      инициализации его нет ни у машины сервиса, ни в шаблоне копии.
//   2. У машины нет личности. Агент берёт IAM-токен из метаданных машины, а
//      для этого к машине должен быть привязан сервисный аккаунт. В
//      instance_template из scaling.tf и autoscale.tf в metadata лежат только
//      ssh-keys и user-data, а service_account_id задан у группы, не у машин:
//      он даёт группе право создавать машины и ничего не говорит о личности
//      самой машины.
//   3. Прав тоже нет. У сервисного аккаунта перечислены compute.editor,
//      vpc.admin и load-balancer.admin (смотри описание
//      scaling_service_account_id в variables.tf). Для записи показателей
//      нужна роль monitoring.editor, для чтения их автоматическим
//      масштабированием monitoring.viewer.
//   4. Имя показателя не сверено. Правило custom_rule ищет
//      pii_capacity_used_ratio в service = "custom". Под каким именем и в
//      каком пространстве показатель окажется в Monitoring после переноса,
//      видно только в самом Monitoring.
//
// Почему здесь не лежит готовый файл настроек агента. Написать его можно за
// десять минут, проверить нельзя: у сборки нет доступа к облаку. Непроверенная
// настройка в репозитории хуже её отсутствия, потому что выглядит работающей и
// разбираться с ней придётся в тот момент, когда группа не выросла под
// нагрузкой. Поэтому здесь список работ, а не настройка.
//
// Порядок включения, когда доступ к облаку появится. Каждый шаг проверяется
// отдельно, следующий начинается только после проверки предыдущего:
//
//   1. Привязать сервисный аккаунт к машинам: service_account_id в
//      instance_template (scaling.tf, autoscale.tf, файлы другой темы).
//      Проверка: с машины отдаётся IAM-токен из метаданных.
//   2. Выдать этому аккаунту monitoring.editor и monitoring.viewer.
//      Проверка: запись показателя вручную через API проходит.
//   3. Поставить и настроить Unified Agent в облачной инициализации: сбор с
//      127.0.0.1 по ручке /metrics раз в 15 с. Проверка: агент жив, ошибок
//      отправки в его журнале нет.
//   4. Найти показатель в Monitoring и сверить имя и метки с тем, что ищет
//      custom_rule. Проверка: показатель виден в обозревателе Monitoring.
//   5. Только теперь autoscale_metrics_delivered = true и
//      autoscale_signal = "capacity". Проверка: группа растёт под нагрузкой,
//      а не стоит в минимальном размере.
//
// Пока этого нет, честный рабочий вариант один: autoscale_signal = "cpu".
// Загрузка процессора доезжает до облака сама, без переносчика, и на нашем
// профиле нагрузки она отличается от занятости ёмкости не настолько, чтобы
// ради разницы включать непроверенную цепочку.

output "metrics_delivery" {
  description = "Состояние доставки показателей сервиса в Yandex Monitoring: от неё зависит, можно ли включать рост группы по показателю сервиса"
  value = {
    state = var.autoscale_metrics_delivered ? "заявлена настроенной переменной autoscale_metrics_delivered" : "не настроена: переносчика с ручки /metrics в Yandex Monitoring на стенде нет"
    signal_in_use = var.enable_autoscale_signal ? (
      var.autoscale_signal == "capacity" ? "рост по показателю сервиса pii_capacity_used_ratio" : "рост по загрузке процессора, запасной вариант"
    ) : (var.enable_scaling ? "рост по загрузке процессора, группа из scaling.tf" : "масштабирование выключено")
    missing = var.autoscale_metrics_delivered ? "по заявлению настроено" : "агент переноса, сервисный аккаунт на самой машине, роли monitoring.editor и monitoring.viewer, сверка имени показателя"
    how     = "порядок включения расписан комментарием в monitoring.tf, разбор в docs/SCALING.md"
  }
}
