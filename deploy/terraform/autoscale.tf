// Автоматическое масштабирование по сигналу самого сервиса.
//
// Чем этот файл отличается от scaling.tf. В scaling.tf группа растёт по
// загрузке процессора: это то, что облако умеет без единой дополнительной
// настройки, и для первого включения это правильный выбор. Здесь описан
// второй вариант той же группы, который растёт по показателю сервиса
// pii_capacity_used_ratio, то есть по доле занятой модельной ёмкости разбора.
// Причины выбора и расчёты собраны в docs/SCALING.md, коротко три.
//
// Первая: загрузка процессора считает всю машину, включая сборку сервиса при
// выкладке, наблюдение и обслуживание. Сигнал сервиса считает только разбор
// текста, поэтому выкладка новой версии не приводит к росту группы.
//
// Вторая: загрузка процессора упирается в сто процентов и после этого не
// отличает перегрузку в два раза от перегрузки в десять, то есть не может
// сказать, сколько копий добавить. Занятость ёмкости выражена в тех же
// единицах, что и модель ёмкости, и прямо переводится в число копий: нужное
// число равно текущему, умноженному на отношение сигнала к цели.
//
// Третья: сигнал сервиса сразу учитывает размер текста. Пропускная способность
// у нас определяется объёмом текста, а не числом запросов, и одна и та же
// частота запросов при 250 байтах и при 8 килобайтах отличается по стоимости в
// двадцать пять раз.
//
// Два варианта группы одновременно включать нельзя: это одна и та же роль в
// одной зоне и одно и то же имя цели балансировщика. Проверяется предусловием.
// По умолчанию файл ничего не создаёт: enable_autoscale_signal = false.

// --- Переменные ---

variable "enable_autoscale_signal" {
  description = "Включить группу машин, которая растёт по показателю сервиса, а не по загрузке процессора. Взаимно исключает enable_scaling."
  type        = bool
  default     = false
}

variable "autoscale_signal" {
  description = "Сигнал роста. Значение capacity означает занятость модельной ёмкости разбора вместе с ожиданием в очереди, значение cpu означает загрузку процессора как запасной вариант, когда показатели сервиса не доезжают до Yandex Monitoring."
  type        = string
  default     = "capacity"

  validation {
    condition     = contains(["capacity", "cpu"], var.autoscale_signal)
    error_message = "Допустимые значения: capacity либо cpu."
  }
}

variable "autoscale_metrics_delivered" {
  description = "Подтверждение, что показатели сервиса доезжают в Yandex Monitoring под именем pii_capacity_used_ratio. Без доставки группа по сигналу capacity не увидит метрику и останется в минимальном размере. Способ доставки описан в docs/SCALING.md."
  type        = bool
  default     = false
}

variable "autoscale_capacity_target" {
  description = "Целевая занятость модельной ёмкости копии, доля от единицы. 0.6 означает, что в установившемся режиме копия занята разбором на шестьдесят процентов, а оставшиеся сорок держатся под всплеск на время появления новой копии."
  type        = number
  default     = 0.6

  validation {
    condition     = var.autoscale_capacity_target > 0.2 && var.autoscale_capacity_target <= 0.9
    error_message = "Цель занятости задаётся долей от 0.2 до 0.9: ниже растёт стоимость простоя, выше не остаётся запаса на время появления копии."
  }
}

variable "autoscale_queue_wait_target" {
  description = "Целевая доля 95 ожидания места в ограничителе, секунды. На штатной нагрузке ожидание неразличимо: вся задержка, доля 99, равна 0.97 мс. Расчётное ожидание на замеренном потолке около 8 мс, поэтому 5 мс это уже признак перегрузки."
  type        = number
  default     = 0.005

  validation {
    condition     = var.autoscale_queue_wait_target > 0 && var.autoscale_queue_wait_target <= 0.5
    error_message = "Порог ожидания задаётся в секундах и не может превышать предел ожидания в ограничителе, равный 0.5 с."
  }
}

variable "autoscale_measurement_duration" {
  description = "Окно усреднения сигнала перед решением, секунды"
  type        = number
  default     = 60

  validation {
    condition     = var.autoscale_measurement_duration >= 60 && var.autoscale_measurement_duration <= 600
    error_message = "Облако принимает окно усреднения от 60 до 600 секунд."
  }
}

variable "autoscale_warmup_duration" {
  description = "Время прогрева новой копии, в течение которого её показатели не учитываются, секунды. Копия собирается из исходников около двух минут, поэтому по умолчанию 180."
  type        = number
  default     = 180

  validation {
    condition     = var.autoscale_warmup_duration >= 0 && var.autoscale_warmup_duration <= 600
    error_message = "Время прогрева задаётся от 0 до 600 секунд."
  }
}

variable "autoscale_stabilization_duration" {
  description = "Окно, в течение которого группа не уменьшается, секунды. Защита от раскачки: решение об уменьшении принимается по максимуму сигнала за это окно."
  type        = number
  default     = 300

  validation {
    condition     = var.autoscale_stabilization_duration >= 60 && var.autoscale_stabilization_duration <= 1800
    error_message = "Окно устойчивости задаётся от 60 до 1800 секунд."
  }
}

variable "autoscale_metric_folder_id" {
  description = "Каталог, в котором лежат показатели сервиса в Yandex Monitoring. Пусто означает тот же каталог, что и стенд."
  type        = string
  default     = ""
}

variable "autoscale_create_balancer" {
  description = "Создавать простую точку входа для этой группы. Ставится в false, когда точка входа из balancer.tf переключена на группу с ростом по сигналу: две точки входа перед одним сервисом не нужны."
  type        = bool
  default     = true
}

variable "autoscale_metric_labels" {
  description = "Метки, по которым выбирается показатель в Yandex Monitoring. Пусто означает выбор только по имени показателя."
  type        = map(string)
  default     = {}
}

// Плановая нагрузка. Из неё считается начальный размер группы: подниматься с
// двух копий и ждать три решения подряд, когда заранее известно, что нужно
// шесть, значит потратить на разгон четверть часа.

variable "plan_rps" {
  description = "Плановая нагрузка в запросах в секунду, по ней считается начальный размер группы"
  type        = number
  default     = 1000

  validation {
    condition     = var.plan_rps > 0
    error_message = "Плановая нагрузка должна быть положительной."
  }
}

variable "plan_text_bytes" {
  description = "Плановый средний размер текста в байтах. В наборе для проверки среднее 247 байт."
  type        = number
  default     = 250

  validation {
    condition     = var.plan_text_bytes > 0
    error_message = "Размер текста должен быть положительным."
  }
}

// --- Модель ёмкости ---
//
// Та же формула, что в docs/SCALING.md и в internal/metrics/metrics.go.
// Стоимость запроса в процессорном времени одного ядра:
//
//   C = 0.57 мс + 1.56 мс на килобайт текста, для текстов длиннее 2 КБ умножить на 1.8
//
// Коэффициенты подобраны по двум замеренным точкам потолка на стенде: 23 200
// запросов в секунду на текстах по 250 байт и 5 980 на текстах по 2 КБ, обе
// при 22 занятых ядрах из 24. На двух других замеренных точках формула
// расходится с замером на 8 процентов при 500 байтах и на 1 процент при 8 КБ.

locals {
  capacity_fixed_ms    = 0.57
  capacity_per_kib_ms  = 1.56
  capacity_long_bytes  = 2048
  capacity_long_factor = 1.8

  // Доля ядер машины, которую сервис реально занимает разбором на потолке:
  // 92 процента, то есть 22 ядра из 24. Замер, а не оценка.
  capacity_usable_fraction = 0.92

  // Стоимость одного планового запроса в миллисекундах процессорного времени.
  plan_cost_ms = (local.capacity_fixed_ms + local.capacity_per_kib_ms * var.plan_text_bytes / 1024) * (
    var.plan_text_bytes > local.capacity_long_bytes ? local.capacity_long_factor : 1
  )

  // Сколько ядер нужно занять под плановую нагрузку и сколько ядер одна копия
  // отдаёт под неё при выбранной цели занятости.
  plan_cores_demand   = var.plan_rps * local.plan_cost_ms / 1000
  plan_cores_per_copy = var.scaling_cores * local.capacity_usable_fraction * var.autoscale_capacity_target
  plan_copies         = max(var.scaling_min, ceil(local.plan_cores_demand / local.plan_cores_per_copy))

  // Потолок одной копии и всей группы в запросах в секунду при плановом размере
  // текста. Потолок это сто процентов занятости, рабочая точка это цель.
  plan_copy_rps_ceiling  = floor(var.scaling_cores * local.capacity_usable_fraction * 1000 / local.plan_cost_ms)
  plan_group_rps_ceiling = local.plan_copy_rps_ceiling * var.scaling_max

  autoscale_metric_folder = var.autoscale_metric_folder_id != "" ? var.autoscale_metric_folder_id : var.folder_id

  // Правила роста. Облако считает рекомендацию по каждому правилу и берёт
  // наибольшую, поэтому правила дополняют друг друга, а не спорят.
  //
  // Основное правило проактивное: занятость ёмкости растёт задолго до того,
  // как появляется очередь, и позволяет добавить копию заранее.
  // Второе правило аварийное: занятость ёмкости, как и загрузка процессора,
  // упирается в единицу, и после этого отличить двукратную перегрузку от
  // десятикратной может только ожидание в очереди.
  //
  // Оба правила типа UTILIZATION: показатель снимается с каждой копии
  // отдельно, облако сравнивает с целью среднее по копиям и делит нагрузку на
  // цель. Тип WORKLOAD здесь не подходит: он рассчитан на показатель, общий
  // для всей группы, например длину внешней очереди.
  autoscale_rules = var.autoscale_signal == "cpu" ? [] : [
    {
      name   = "pii_capacity_used_ratio"
      target = var.autoscale_capacity_target
    },
    {
      name   = "pii_queue_wait_p95_seconds"
      target = var.autoscale_queue_wait_target
    },
  ]
}

// --- Группа машин ---

resource "yandex_compute_instance_group" "app_signal" {
  count = var.enable_autoscale_signal ? 1 : 0

  name               = "pii-guard-asg"
  description        = "Копии сервиса маскирования, рост по показателю сервиса"
  folder_id          = var.folder_id
  service_account_id = var.scaling_service_account_id

  // Сколько группа ждёт первой удачной проверки живости у новой копии. Дальше
  // машина считается сломанной и пересоздаётся.
  max_checking_health_duration = var.scaling_max_checking_health_seconds

  instance_template {
    platform_id = "standard-v3"
    name        = "pii-guard-asg-{instance.index}"
    hostname    = "pii-guard-asg-{instance.index}"

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
      // Начальный размер считается по плановой нагрузке, а не берётся
      // минимальным: разгон с двух копий до шести занял бы три решения подряд,
      // то есть около пятнадцати минут, и всё это время нагрузка стояла бы в
      // очереди.
      initial_size  = local.plan_copies
      min_zone_size = var.scaling_min
      max_size      = var.scaling_max

      measurement_duration   = var.autoscale_measurement_duration
      warmup_duration        = var.autoscale_warmup_duration
      stabilization_duration = var.autoscale_stabilization_duration

      // Загрузка процессора остаётся запасным сигналом: она не требует
      // доставки показателей в Yandex Monitoring и работает сразу.
      cpu_utilization_target = var.autoscale_signal == "cpu" ? var.scaling_cpu_target : null

      dynamic "custom_rule" {
        for_each = local.autoscale_rules
        content {
          rule_type   = "UTILIZATION"
          metric_type = "GAUGE"
          metric_name = custom_rule.value.name
          target      = custom_rule.value.target
          service     = "custom"
          folder_id   = local.autoscale_metric_folder
          labels      = var.autoscale_metric_labels
        }
      }
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

    // Пауза после запуска машины: копия ставит Go и собирает сервис, до конца
    // сборки её ответы ничего не значат.
    startup_duration = var.scaling_startup_seconds
  }

  // Группа смотрит на живость, а не на готовность: настройки те же, что у
  // группы из scaling.tf, разбор решения в docs/BALANCING.md. Если сюда
  // поставить /readyz, каждое мягкое завершение копии группа примет за поломку
  // и превратит в двухминутную пересборку машины.
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

  load_balancer {
    target_group_name        = "pii-guard-asg-targets"
    target_group_description = "Копии сервиса маскирования, рост по показателю сервиса"
  }

  lifecycle {
    precondition {
      condition     = !var.enable_scaling
      error_message = "Одновременно включены две группы машин: enable_scaling в scaling.tf и enable_autoscale_signal здесь. Оставьте одну."
    }

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
      condition     = var.autoscale_signal == "cpu" || var.autoscale_metrics_delivered
      error_message = "Сигнал capacity требует доставки показателей сервиса в Yandex Monitoring. Настройте доставку по docs/SCALING.md и поставьте autoscale_metrics_delivered = true либо выберите autoscale_signal = cpu."
    }

    precondition {
      condition     = local.plan_copies <= var.scaling_max
      error_message = "Плановая нагрузка требует больше копий, чем разрешает scaling_max. Поднимите scaling_max, увеличьте scaling_cores либо уменьшите plan_rps."
    }
  }
}

// Точка входа для этой группы. Нарочно простая: разбор выбора между сетевым и
// прикладным балансировщиком, сливом соединений и порогом паники ведётся в
// balancer.tf, и та точка входа привязана к группе из scaling.tf. Пока
// включён рост по сигналу, работает эта; после объединения двух вариантов
// группы в один достаточно поставить autoscale_create_balancer = false.
//
// Проверка здесь на готовность (/readyz), а не на живость: балансировщик
// решает, слать ли трафик, и копия сама просит увести с неё запросы, когда
// завершает работу.
resource "yandex_lb_network_load_balancer" "app_signal" {
  count = var.enable_autoscale_signal && var.autoscale_create_balancer ? 1 : 0

  name = "pii-guard-asg-balancer"
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
    target_group_id = yandex_compute_instance_group.app_signal[0].load_balancer[0].target_group_id

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

// --- Что модуль сообщает о плане ---

output "autoscale_capacity_plan" {
  description = "Расчёт ёмкости по плановой нагрузке: стоимость запроса, потребность в ядрах, начальный размер группы и потолок"
  value = {
    plan_rps                 = var.plan_rps
    plan_text_bytes          = var.plan_text_bytes
    request_cost_core_ms     = local.plan_cost_ms
    cores_demand             = local.plan_cores_demand
    cores_per_copy_at_target = local.plan_cores_per_copy
    copies_initial           = local.plan_copies
    copy_rps_ceiling         = local.plan_copy_rps_ceiling
    group_rps_ceiling        = local.plan_group_rps_ceiling
    signal                   = var.autoscale_signal
  }
}

output "autoscale_balancer_address" {
  description = "Внешний адрес балансировщика группы, растущей по показателю сервиса"
  value = one([
    for spec in try(yandex_lb_network_load_balancer.app_signal[0].listener, []) :
    one([for addr in spec.external_address_spec : addr.address])
  ])
}
