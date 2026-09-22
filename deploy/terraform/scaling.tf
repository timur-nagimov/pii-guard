// Горизонтальное масштабирование стенда.
//
// Блок выключен по умолчанию (enable_scaling = false) и включается одной
// переменной. Причина, по которой он выключен на время хакатона, лежит не в
// инфраструктуре, а в сервисе: соответствие «маска — исходный текст» хранится
// в памяти процесса. Пока хранилище не вынесено в общее, вторая машина не
// сможет выполнить обратное преобразование для записи, созданной первой.
//
// Порядок включения: перевести хранилище на общее (интерфейс для этого уже
// выделен в internal/store), затем задать enable_scaling = true.

resource "yandex_compute_instance_group" "app" {
  count = var.enable_scaling ? 1 : 0

  name               = "pii-guard-group"
  folder_id          = var.folder_id
  service_account_id = var.scaling_service_account_id

  instance_template {
    platform_id = "standard-v3"

    resources {
      cores         = var.app_cores
      memory        = var.app_memory_gb
      core_fraction = 100
    }

    boot_disk {
      initialize_params {
        image_id = data.yandex_compute_image.ubuntu.id
        type     = "network-ssd"
        size     = 60
      }
    }

    network_interface {
      subnet_ids         = [var.subnet_id]
      nat                = true
      security_group_ids = [yandex_vpc_security_group.stand.id]
    }

    metadata = {
      ssh-keys  = "ubuntu:${var.ssh_public_key}"
      user-data = file("${path.module}/cloud-init-app.yaml")
    }
  }

  scale_policy {
    auto_scale {
      initial_size           = var.scaling_min
      min_zone_size          = var.scaling_min
      max_size               = var.scaling_max
      measurement_duration   = 60
      warmup_duration        = 60
      stabilization_duration = 120

      // Масштабируем по загрузке процессора: она прямо отражает объём
      // разбора текста, потому что сервис ничего не ждёт по сети.
      cpu_utilization_target = 60
    }
  }

  allocation_policy {
    zones = [var.zone]
  }

  deploy_policy {
    max_unavailable = 1
    max_expansion   = 1
  }

  health_check {
    interval = 5
    timeout  = 2
    http_options {
      port = 8080
      path = "/readyz"
    }
  }

  load_balancer {
    target_group_name = "pii-guard-targets"
  }
}

// Сетевой балансировщик перед группой машин.
resource "yandex_lb_network_load_balancer" "app" {
  count = var.enable_scaling ? 1 : 0

  name = "pii-guard-balancer"

  listener {
    name        = "http"
    port        = 80
    target_port = 8080
    external_address_spec {
      ip_version = "ipv4"
    }
  }

  attached_target_group {
    target_group_id = yandex_compute_instance_group.app[0].load_balancer[0].target_group_id

    healthcheck {
      name = "readyz"
      http_options {
        port = 8080
        path = "/readyz"
      }
    }
  }
}
