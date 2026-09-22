// Горизонтальное масштабирование стенда.
//
// Блок выключен по умолчанию (enable_scaling = false) и включается одной
// переменной. Причина, по которой он выключен на время проверки, лежит не в
// инфраструктуре, а в арифметике: один проход детекторов стоит около 1.2 мс на
// килобайт текста на ядро, потолок одной машины 23 тысячи запросов в секунду,
// а контракт требует тысячу. Запас двадцатикратный, автоматическое
// масштабирование не сработало бы ни разу, а балансировщик и вторая машина
// добавили бы точек отказа там, где отказ стоит дороже всего.
//
// ВАЖНО: группа машин требует включённого общего хранилища
// (enable_shared_store = true). Копии сервиса обслуживают запросы по очереди,
// и обратное преобразование приходит не на ту копию, которая делала маску.
// Пока хранилище в памяти процесса, ответом будет 404. Это проверяется
// предусловием ниже: включить масштабирование без хранилища модуль не даст.
//
// Порядок включения:
//   1. enable_shared_store = true, дождаться, пока хранилище поднимется;
//   2. enable_monitor_vm  = true, чтобы показатели пережили смену копий;
//   3. enable_scaling     = true и scaling_service_account_id.

locals {
  // Копии в группе разворачиваются с нуля, поэтому источник сервиса обязателен:
  // без него машина поднимется, а сервиса на ней не будет.
  app_source_defined = var.app_binary_url != "" || var.app_source_url != "" || var.app_repo_url != ""

  // Наблюдение на копиях не разворачивается: за группой следит отдельная
  // машина наблюдения, она же держит историю показателей.
  user_data_group = templatefile("${local.cloud_init_dir}/app.yaml.tftpl", {
    sysctl_b64        = filebase64("${local.cloud_init_dir}/sysctl-highload.conf")
    limits_b64        = filebase64("${local.cloud_init_dir}/limits-nofile.conf")
    deploy_env_b64    = base64encode(local.deploy_env_app)
    deploy_script_b64 = filebase64("${local.cloud_init_dir}/deploy-app.sh")
    config_b64        = base64encode(local.app_config)
    unit_b64          = filebase64("${local.cloud_init_dir}/pii-guard.service")
    monitoring        = false
    monitoring_files  = {}
  })
}

resource "yandex_compute_instance_group" "app" {
  count = var.enable_scaling ? 1 : 0

  name               = "pii-guard-group"
  description        = "Копии сервиса маскирования под сетевым балансировщиком"
  folder_id          = var.folder_id
  service_account_id = var.scaling_service_account_id

  instance_template {
    platform_id = "standard-v3"
    name        = "pii-guard-{instance.index}"
    hostname    = "pii-guard-{instance.index}"

    resources {
      cores         = var.scaling_cores
      memory        = var.scaling_memory_gb
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
      subnet_ids = [local.subnet_id]
      nat        = true
      security_group_ids = concat(
        [yandex_vpc_security_group.stand.id],
        yandex_vpc_security_group.balancer[*].id,
      )
    }

    metadata = {
      ssh-keys  = "ubuntu:${var.ssh_public_key}"
      user-data = local.user_data_group
    }
  }

  scale_policy {
    auto_scale {
      initial_size           = var.scaling_min
      min_zone_size          = var.scaling_min
      max_size               = var.scaling_max
      measurement_duration   = 60
      warmup_duration        = 120
      stabilization_duration = 180

      // Масштабируем по загрузке процессора: она прямо отражает объём разбора
      // текста, потому что сервис ничего не ждёт по сети. Порог 60 процентов
      // оставляет время на прогрев новой копии: сборка и запуск занимают
      // около двух минут, за них нагрузка успеет вырасти ещё в полтора раза.
      cpu_utilization_target = var.scaling_cpu_target
    }
  }

  allocation_policy {
    zones = [var.zone]
  }

  deploy_policy {
    // Обновление по одной копии: группа не должна проседать по ёмкости, пока
    // катится новая версия.
    max_unavailable = 1
    max_expansion   = 1
    max_creating    = 2
    max_deleting    = 1
  }

  // Копия считается живой, только когда отвечает /readyz: сборка и запуск
  // занимают около двух минут, и до этого момента трафик слать некуда.
  health_check {
    interval            = 5
    timeout             = 2
    healthy_threshold   = 2
    unhealthy_threshold = 5

    http_options {
      port = var.app_http_port
      path = "/readyz"
    }
  }

  load_balancer {
    target_group_name        = "pii-guard-targets"
    target_group_description = "Копии сервиса маскирования"
  }

  lifecycle {
    precondition {
      condition     = var.enable_shared_store
      error_message = "Масштабирование без общего хранилища даёт 404 на обратном преобразовании: поставьте enable_shared_store = true."
    }

    precondition {
      condition     = var.enable_monitor_vm
      error_message = "В режиме масштабирования наблюдение должно жить на отдельной машине: поставьте enable_monitor_vm = true."
    }

    precondition {
      condition     = var.scaling_service_account_id != ""
      error_message = "Группе машин нужен сервисный аккаунт: задайте scaling_service_account_id."
    }

    precondition {
      condition     = local.app_source_defined
      error_message = "Копии разворачиваются с нуля: задайте app_binary_url, app_source_url либо app_repo_url."
    }

    precondition {
      condition     = var.scaling_min <= var.scaling_max
      error_message = "scaling_min не может быть больше scaling_max."
    }
  }
}

// Сетевой балансировщик перед группой машин. Сетевой, а не прикладной:
// он работает на уровне соединений и не добавляет разбора HTTP, то есть не
// отъедает у доли 99 те десятые доли миллисекунды, ради которых всё считалось.
resource "yandex_lb_network_load_balancer" "app" {
  count = var.enable_scaling ? 1 : 0

  name = "pii-guard-balancer"
  type = "external"

  listener {
    name        = "http"
    port        = 80
    target_port = var.app_http_port

    external_address_spec {
      ip_version = "ipv4"
    }
  }

  attached_target_group {
    target_group_id = yandex_compute_instance_group.app[0].load_balancer[0].target_group_id

    healthcheck {
      name                = "readyz"
      interval            = 5
      timeout             = 2
      healthy_threshold   = 2
      unhealthy_threshold = 5

      http_options {
        port = var.app_http_port
        path = "/readyz"
      }
    }
  }
}
