// Балансировка нагрузки перед группой копий сервиса.
//
// Здесь собрано всё, что относится к точке входа: внешний адрес, выбор уровня
// балансировки, проверки готовности, согласование таймаутов. Сама группа копий
// описана в scaling.tf, разделение сделано намеренно: уровень балансировки
// меняется одной переменной и не трогает описание машин.
//
// ВЫБОР УРОВНЯ. Описаны оба варианта, включён один, переключение переменной
// balancer_layer:
//
//   network:     сетевой балансировщик, уровень соединений. Значение по
//                 умолчанию. Тело запроса через него не проходит: он выбирает
//                 копию при установке соединения и дальше только передаёт
//                 пакеты. Наши тела доходят до четырёх мегабайт, а доля 99
//                 задержки на рабочей частоте равна 0.97 мс, поэтому лишний
//                 разбор HTTP стоил бы дороже самой обработки. Плюс он
//                 пропускает TLS насквозь: порт 443 с самоподписанным
//                 сертификатом продолжает работать без сертификата в
//                 Certificate Manager.
//
//   application: прикладной балансировщик, уровень запросов. Выключен. Даёт
//                 то, чего сетевой дать не может: выбор наименее загруженной
//                 копии на каждый запрос, отдельный таймаут на маршрут прокси
//                 к языковой модели, свой отказ 502 вместо оборванного
//                 соединения. Платим за это разбором HTTP и буферизацией тела,
//                 а порт 443 требует сертификата в Certificate Manager.
//
// Когда переходить на прикладной, разобрано в docs/BALANCING.md.

// --- Уровень балансировки ---

variable "balancer_layer" {
  description = "Уровень балансировки: network это сетевой балансировщик на уровне соединений, application это прикладной на уровне запросов HTTP"
  type        = string
  default     = "network"

  validation {
    condition     = contains(["network", "application"], var.balancer_layer)
    error_message = "Допустимые значения: network или application."
  }
}

variable "balancer_https" {
  description = "Пробрасывать через сетевой балансировщик порт 443. Работает только на уровне соединений: TLS завершает сам сервис своим сертификатом. Прикладной балансировщик так не умеет, ему нужен сертификат в Certificate Manager."
  type        = bool
  default     = true
}

// --- Проверка готовности, по ней балансировщик решает, слать ли трафик ---

variable "balancer_check_path" {
  description = "Адрес проверки готовности. Именно готовность, а не живость: при завершении работы сервис заранее отвечает по нему кодом 503 и тем самым просит вывести себя из обслуживания."
  type        = string
  default     = "/readyz"
}

variable "balancer_check_interval" {
  description = "Период проверки готовности в секундах"
  type        = number
  default     = 2

  validation {
    condition     = var.balancer_check_interval >= 2 && var.balancer_check_interval <= 300
    error_message = "Период проверки задаётся в секундах, от 2 до 300."
  }
}

variable "balancer_check_timeout" {
  description = "Предел ожидания ответа на проверку в секундах. Обработчик готовности не ходит ни в хранилище, ни в детекторы, поэтому предел стоит далеко от реального времени ответа."
  type        = number
  default     = 1

  validation {
    condition     = var.balancer_check_timeout >= 1 && var.balancer_check_timeout <= 60
    error_message = "Предел ожидания задаётся в секундах, от 1 до 60."
  }
}

variable "balancer_healthy_threshold" {
  description = "Сколько успешных проверок подряд возвращают копию в обслуживание"
  type        = number
  default     = 2

  validation {
    condition     = var.balancer_healthy_threshold >= 2 && var.balancer_healthy_threshold <= 10
    error_message = "Порог возврата задаётся числом проверок, от 2 до 10."
  }
}

variable "balancer_unhealthy_threshold" {
  description = "Сколько неудачных проверок подряд выводят копию из обслуживания. Чем меньше, тем быстрее уходит трафик с умирающей копии и тем меньше запросов теряется."
  type        = number
  default     = 2

  validation {
    condition     = var.balancer_unhealthy_threshold >= 2 && var.balancer_unhealthy_threshold <= 10
    error_message = "Порог вывода задаётся числом проверок, от 2 до 10."
  }
}

variable "balancer_drain_margin" {
  description = "Запас в секундах поверх расчётного времени, за которое балансировщик замечает снятие готовности. Из суммы получается, сколько сервис обязан доживать после сигнала остановки, прежде чем закрывать приём."
  type        = number
  default     = 2
}

// --- Прикладной балансировщик: только когда balancer_layer = application ---

variable "balancer_mode" {
  description = "Способ выбора копии на прикладном уровне. LEAST_REQUEST отдаёт запрос копии с наименьшим числом активных запросов и потому переносит наш разброс стоимости обработки. ROUND_ROBIN раскладывает поровну по числу запросов, а не по объёму работы."
  type        = string
  default     = "LEAST_REQUEST"

  validation {
    condition     = contains(["ROUND_ROBIN", "LEAST_REQUEST", "RANDOM", "MAGLEV_HASH"], var.balancer_mode)
    error_message = "Допустимые значения: ROUND_ROBIN, LEAST_REQUEST, RANDOM, MAGLEV_HASH."
  }
}

variable "balancer_panic_threshold" {
  description = "Доля живых копий в процентах, ниже которой прикладной балансировщик начинает слать трафик во все копии подряд, не глядя на проверки. Ноль означает поведение по умолчанию: лучше отдать запрос сомнительной копии, чем не отдать никому."
  type        = number
  default     = 0
}

variable "balancer_route_timeout" {
  description = "Предел обработки одного запроса на прикладном балансировщике. Длиннее таймаута записи ответа в сервисе и короче клиентского: сервис сдаётся первым, балансировщик успевает превратить обрыв в осмысленный код ответа."
  type        = string
  default     = "9500ms"
}

variable "balancer_proxy_route_timeout" {
  description = "Предел обработки на маршруте прокси к языковой модели. Отдельный, потому что там ждут не разбора текста, а ответа модели. Сетевой балансировщик так не умеет: он не знает, какой это путь."
  type        = string
  default     = "60s"
}

variable "balancer_session_affinity" {
  description = "Привязывать клиента к копии по исходному адресу. Выключено осознанно: проверяющая система приходит с одного адреса, и привязка свела бы группу к одной работающей копии. Соответствия между копиями связывает общее хранилище, а не привязка."
  type        = bool
  default     = false
}

locals {
  lb_network = var.enable_scaling && var.balancer_layer == "network"
  lb_app     = var.enable_scaling && var.balancer_layer == "application"

  // Худшее время, за которое балансировщик замечает снятие готовности:
  // последняя удачная проверка прошла только что, дальше нужно набрать
  // balancer_unhealthy_threshold неудач, и последняя из них может тянуться до
  // предела ожидания.
  lb_detect_seconds = var.balancer_check_interval * var.balancer_unhealthy_threshold + var.balancer_check_timeout

  // Столько сервис обязан продолжать принимать запросы после сигнала
  // остановки, уже отвечая на проверку готовности кодом 503. Раньше закрывать
  // приём нельзя: балансировщик ещё считает копию живой и шлёт на неё трафик.
  lb_drain_seconds = local.lb_detect_seconds + var.balancer_drain_margin

  // Время возврата копии в обслуживание после того, как она стала готова.
  lb_join_seconds = var.balancer_check_interval * var.balancer_healthy_threshold
}

// Внешний адрес точки входа. Заводится отдельным ресурсом, а не выдаётся
// балансировщику на лету, по двум причинам: адрес переживает пересоздание
// балансировщика, и оба слушателя сетевого балансировщика, 80 и 443, садятся
// на один и тот же адрес. Без этого у портов оказались бы разные адреса и
// контракт проверки распался бы на два.
resource "yandex_vpc_address" "balancer" {
  count = var.enable_scaling ? 1 : 0

  name        = "pii-guard-balancer-ip"
  description = "Точка входа группы копий сервиса маскирования"

  external_ipv4_address {
    zone_id = var.zone
  }
}

// --- Уровень соединений: включён по умолчанию ---

resource "yandex_lb_network_load_balancer" "app" {
  count = local.lb_network ? 1 : 0

  name        = "pii-guard-balancer"
  description = "Сетевой балансировщик перед группой копий сервиса маскирования"
  type        = "external"

  listener {
    name        = "http"
    port        = var.app_http_port
    target_port = var.app_http_port

    external_address_spec {
      address    = yandex_vpc_address.balancer[0].external_ipv4_address[0].address
      ip_version = "ipv4"
    }
  }

  // Второй слушатель на том же адресе. Соединение передаётся насквозь, TLS
  // завершает сам сервис, поэтому сертификат и его закрепление не меняются.
  dynamic "listener" {
    for_each = var.balancer_https ? [1] : []
    content {
      name        = "https"
      port        = var.app_https_port
      target_port = var.app_https_port

      external_address_spec {
        address    = yandex_vpc_address.balancer[0].external_ipv4_address[0].address
        ip_version = "ipv4"
      }
    }
  }

  attached_target_group {
    target_group_id = yandex_compute_instance_group.app[0].load_balancer[0].target_group_id

    // Проверка идёт по готовности и только по HTTP, даже когда трафик ходит по
    // обоим портам: готовность у копии одна, проверять её дважды незачем.
    healthcheck {
      name                = "readyz"
      interval            = var.balancer_check_interval
      timeout             = var.balancer_check_timeout
      healthy_threshold   = var.balancer_healthy_threshold
      unhealthy_threshold = var.balancer_unhealthy_threshold

      http_options {
        port = var.app_http_port
        path = var.balancer_check_path
      }
    }
  }

  lifecycle {
    precondition {
      condition     = var.balancer_check_timeout < var.balancer_check_interval
      error_message = "Предел ожидания проверки должен быть короче периода между проверками, иначе проверки наезжают друг на друга."
    }

    precondition {
      condition     = var.enable_shared_store
      error_message = "Балансировщик без общего хранилища разведёт маскирование и обратное преобразование по разным копиям, и ответ будет формально верным, но неправильным. Поставьте enable_shared_store = true."
    }
  }
}

// --- Уровень запросов: описан, выключен ---
//
// Собирается из четырёх ресурсов: группа бэкендов знает, как выбирать копию и
// как её проверять; маршрутизатор и виртуальный узел задают правила по путям;
// сам балансировщик слушает адрес. Включается balancer_layer = "application".

resource "yandex_alb_backend_group" "app" {
  count = local.lb_app ? 1 : 0

  name        = "pii-guard-backends"
  description = "Копии сервиса маскирования за прикладным балансировщиком"
  folder_id   = var.folder_id

  // Привязка клиента к копии. Выключена: разбор причин в docs/BALANCING.md,
  // коротко, она не нужна при общем хранилище и вредна при одном клиенте.
  dynamic "session_affinity" {
    for_each = var.balancer_session_affinity ? [1] : []
    content {
      connection {
        source_ip = true
      }
    }
  }

  http_backend {
    name             = "pii-guard"
    port             = var.app_http_port
    target_group_ids = [yandex_compute_instance_group.app[0].application_load_balancer[0].target_group_id]
    weight           = 1

    load_balancing_config {
      mode            = var.balancer_mode
      panic_threshold = var.balancer_panic_threshold
    }

    healthcheck {
      interval            = "${var.balancer_check_interval}s"
      timeout             = "${var.balancer_check_timeout}s"
      healthy_threshold   = var.balancer_healthy_threshold
      unhealthy_threshold = var.balancer_unhealthy_threshold

      http_healthcheck {
        path = var.balancer_check_path
      }
    }
  }

  lifecycle {
    precondition {
      condition     = var.enable_shared_store
      error_message = "Балансировщик без общего хранилища разведёт маскирование и обратное преобразование по разным копиям, и ответ будет формально верным, но неправильным. Поставьте enable_shared_store = true."
    }
  }
}

resource "yandex_alb_http_router" "app" {
  count = local.lb_app ? 1 : 0

  name        = "pii-guard-router"
  description = "Правила разбора путей перед копиями сервиса маскирования"
  folder_id   = var.folder_id
}

resource "yandex_alb_virtual_host" "app" {
  count = local.lb_app ? 1 : 0

  name           = "pii-guard-host"
  http_router_id = yandex_alb_http_router.app[0].id

  // Порядок маршрутов важен: побеждает первое совпадение. Сначала прокси к
  // языковой модели со своим длинным пределом, потом всё остальное с коротким.
  route {
    name = "upstream"

    http_route {
      http_match {
        path {
          prefix = "/v1/chat/completions"
        }
      }

      http_route_action {
        backend_group_id = yandex_alb_backend_group.app[0].id
        timeout          = var.balancer_proxy_route_timeout
      }
    }
  }

  route {
    name = "default"

    http_route {
      http_match {
        path {
          prefix = "/"
        }
      }

      http_route_action {
        backend_group_id = yandex_alb_backend_group.app[0].id
        timeout          = var.balancer_route_timeout
      }
    }
  }
}

resource "yandex_alb_load_balancer" "app" {
  count = local.lb_app ? 1 : 0

  name        = "pii-guard-alb"
  description = "Прикладной балансировщик перед группой копий сервиса маскирования"
  folder_id   = var.folder_id
  network_id  = local.network_id

  security_group_ids = concat(
    [yandex_vpc_security_group.stand.id],
    yandex_vpc_security_group.balancer[*].id,
  )

  allocation_policy {
    location {
      zone_id   = var.zone
      subnet_id = local.subnet_id
    }
  }

  listener {
    name = "http"

    endpoint {
      address {
        external_ipv4_address {
          address = yandex_vpc_address.balancer[0].external_ipv4_address[0].address
        }
      }
      ports = [var.app_http_port]
    }

    http {
      handler {
        http_router_id = yandex_alb_http_router.app[0].id
      }
    }
  }
}

// --- Что показать человеку про балансировку ---

output "balancer_entry_url" {
  description = "Точка входа группы копий: адрес балансировщика выбранного уровня"
  value = var.enable_scaling ? format(
    "http://%s/process, уровень %s",
    try(yandex_vpc_address.balancer[0].external_ipv4_address[0].address, "адрес ещё не выдан"),
    var.balancer_layer,
  ) : "масштабирование выключено, точка входа это машина сервиса"
}

output "balancer_drain_plan" {
  description = "Расчёт мягкого вывода копии из обслуживания: все числа выведены из настроек проверки, менять их нужно здесь, а не по месту"
  value = {
    detect_seconds = "${local.lb_detect_seconds} с: столько балансировщик в худшем случае не замечает снятия готовности"
    drain_seconds  = "${local.lb_drain_seconds} с: столько сервис обязан принимать запросы после сигнала остановки, прежде чем закрывать приём"
    join_seconds   = "${local.lb_join_seconds} с: столько готовая копия ждёт первого трафика"
    stop_seconds   = "${local.lb_drain_seconds + 15} с: полная остановка копии с учётом таймаута завершения сервиса в 15 с, столько же минимум нужно задать systemd в TimeoutStopSec"
  }
}
