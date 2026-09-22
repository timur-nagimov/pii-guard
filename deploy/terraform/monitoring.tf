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
