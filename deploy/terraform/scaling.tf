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
// Записи в памяти процесса копия не найдёт, и ответом будет не ошибка: сервис
// примет присланную маску за новый текст и замаскирует её ещё раз. Клиент
// получит код 200 и формально правильный ответ, в котором исходного текста
// нет. Это проверяется предусловием ниже: включить масштабирование без
// хранилища модуль не даст.
//
// Порядок включения:
//   1. enable_shared_store = true, дождаться, пока хранилище поднимется;
//   2. enable_monitor_vm  = true, чтобы показатели пережили смену копий;
//   3. enable_scaling     = true и scaling_service_account_id.
//
// Точка входа, проверки готовности и согласование таймаутов вынесены в
// balancer.tf, разбор решений в docs/BALANCING.md.

// --- Проверка живости: по ней группа решает, пересоздавать ли копию ---
//
// Это не та же проверка, по которой балансировщик решает, слать ли трафик.
// Балансировщик смотрит на готовность (/readyz): копия сама просит увести с
// неё трафик, когда завершает работу. Группа смотрит на живость (/healthz):
// отвечает ли процесс вообще. Если поменять их местами, группа начнёт
// пересоздавать копии, которые всего лишь корректно выводятся из обслуживания,
// и каждое мягкое завершение превратится в двухминутную пересборку машины.

variable "scaling_live_path" {
  description = "Адрес проверки живости. Отвечает, пока жив процесс, и не зависит от готовности принимать трафик."
  type        = string
  default     = "/healthz"
}

variable "scaling_live_interval" {
  description = "Период проверки живости в секундах. Заметно реже проверки готовности: цена ошибки здесь пересоздание машины, а не перевод трафика на соседнюю копию."
  type        = number
  default     = 10
}

variable "scaling_live_timeout" {
  description = "Предел ожидания ответа на проверку живости в секундах. С запасом на загруженную машину: на предельной ступени процессор занят на 92 процента."
  type        = number
  default     = 3
}

variable "scaling_live_unhealthy_threshold" {
  description = "Сколько неудачных проверок живости подряд приводят к пересозданию копии"
  type        = number
  default     = 6
}

variable "scaling_live_healthy_threshold" {
  description = "Сколько удачных проверок живости подряд снимают подозрение с копии"
  type        = number
  default     = 2
}

variable "scaling_startup_seconds" {
  description = "Пауза после запуска машины, в течение которой проверки не влияют на раскатку. Копия разворачивается с нуля: установка Go и сборка занимают около двух минут."
  type        = number
  default     = 180
}

variable "scaling_max_checking_health_seconds" {
  description = "Предел ожидания первой удачной проверки живости. Не дождались за это время, значит машина сломана и её надо пересоздать, а не ждать дальше."
  type        = number
  default     = 300
}

variable "scaling_max_opening_traffic_seconds" {
  description = "Предел ожидания того, что балансировщик начнёт слать трафик на новую копию. Считается от запуска машины, поэтому включает сборку сервиса."
  type        = number
  default     = 300
}

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

  // Время полного завершения копии: столько она доживает после сигнала
  // остановки. Считается в balancer.tf из настроек проверки готовности, здесь
  // используется, чтобы группа не отбирала машину раньше, чем сервис доработал
  // текущие запросы.
  group_stop_seconds = local.lb_drain_seconds + 15
}

resource "yandex_compute_instance_group" "app" {
  count = var.enable_scaling ? 1 : 0

  name               = "pii-guard-group"
  description        = "Копии сервиса маскирования под балансировщиком"
  folder_id          = var.folder_id
  service_account_id = var.scaling_service_account_id

  // Сколько группа ждёт первой удачной проверки живости у новой копии.
  // Дальше машина считается сломанной и пересоздаётся.
  max_checking_health_duration = var.scaling_max_checking_health_seconds

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

    // Пауза перед тем, как проверки начнут влиять на раскатку: новая копия
    // ставит Go и собирает сервис, и до конца сборки её ответы ничего не
    // значат.
    startup_duration = var.scaling_startup_seconds

    // Сначала поднять новую копию, потом убрать старую. Обратный порядок
    // экономит квоту, но на время раскатки снимает часть ёмкости.
    strategy = "proactive"
  }

  // Проверка живости. Копия считается сломанной, только когда процесс перестал
  // отвечать подряд scaling_live_unhealthy_threshold раз. Долго и намеренно:
  // пересоздание стоит около двух минут, и запускать его из-за одной
  // просадки дороже, чем подождать.
  health_check {
    interval            = var.scaling_live_interval
    timeout             = var.scaling_live_timeout
    healthy_threshold   = var.scaling_live_healthy_threshold
    unhealthy_threshold = var.scaling_live_unhealthy_threshold

    http_options {
      port = var.app_http_port
      path = var.scaling_live_path
    }
  }

  // Целевая группа под сетевой балансировщик. Создаётся, только когда выбран
  // уровень соединений.
  dynamic "load_balancer" {
    for_each = local.lb_network ? [1] : []
    content {
      target_group_name        = "pii-guard-targets"
      target_group_description = "Копии сервиса маскирования, уровень соединений"

      // Сколько группа ждёт, пока балансировщик начнёт слать трафик на новую
      // копию. Не дождались, значит проверка готовности не проходит и копию
      // надо пересоздать, а не держать в группе пустой.
      max_opening_traffic_duration = var.scaling_max_opening_traffic_seconds

      // Проверки не игнорируем: трафик идёт только на ту копию, которая сама
      // сказала, что готова.
      ignore_health_checks = false
    }
  }

  // Целевая группа под прикладной балансировщик. Создаётся, только когда
  // выбран уровень запросов.
  dynamic "application_load_balancer" {
    for_each = local.lb_app ? [1] : []
    content {
      target_group_name            = "pii-guard-alb-targets"
      target_group_description     = "Копии сервиса маскирования, уровень запросов"
      max_opening_traffic_duration = var.scaling_max_opening_traffic_seconds
      ignore_health_checks         = false
    }
  }

  lifecycle {
    precondition {
      condition     = var.enable_shared_store
      error_message = "Масштабирование без общего хранилища ломает обратное преобразование молча: копия не найдёт чужую запись и замаскирует присланную маску ещё раз, ответив кодом 200. Поставьте enable_shared_store = true."
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

    precondition {
      condition     = var.scaling_live_timeout < var.scaling_live_interval
      error_message = "Предел ожидания проверки живости должен быть короче периода между проверками."
    }

    precondition {
      condition     = var.scaling_max_opening_traffic_seconds >= var.scaling_startup_seconds
      error_message = "Ожидание трафика от балансировщика считается от запуска машины и не может быть короче паузы на сборку сервиса."
    }
  }
}

// Столько времени копия живёт после сигнала остановки: сначала она снимает
// готовность и доживает, пока балансировщик это заметит, потом добивает
// текущие запросы. Значение считается из настроек проверки в balancer.tf и
// выводится наружу, чтобы systemd на машине задавал TimeoutStopSec не на глаз.
output "scaling_stop_seconds" {
  description = "Сколько секунд занимает мягкий вывод копии из обслуживания вместе с завершением сервиса"
  value       = var.enable_scaling ? local.group_stop_seconds : 0
}
