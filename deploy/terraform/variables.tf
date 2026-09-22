// Переменные стенда. Значения по умолчанию описывают то, что реально поднято
// и сверено с облаком: две машины, существующие сеть и подсеть, наблюдение на
// машине сервиса. Всё, что выключено по умолчанию, включается одной
// переменной и не трогает работающий стенд.

// --- Облако и каталог ---

variable "cloud_id" {
  description = "Идентификатор облака"
  type        = string
  default     = "b1g6c8lv8atu28tdgr9i"
}

variable "folder_id" {
  description = "Идентификатор каталога"
  type        = string
  default     = "b1gior9ks9q2nh5h1vgg"
}

variable "zone" {
  description = "Зона доступности"
  type        = string
  default     = "ru-central1-b"
}

// --- Сеть ---

variable "create_network" {
  description = "Создать сеть и подсеть самому модулем. Для разворачивания с нуля в пустом каталоге поставьте true, для работы с существующим стендом оставьте false."
  type        = bool
  default     = false
}

variable "network_id" {
  description = "Идентификатор существующей сети, используется при create_network = false"
  type        = string
  default     = "enpc9oglebtvo74lcd4t"
}

variable "subnet_id" {
  description = "Идентификатор существующей подсети в выбранной зоне, используется при create_network = false"
  type        = string
  default     = "e2l6qlb8r7c1q4prmu1e"
}

variable "network_name" {
  description = "Имя создаваемой сети при create_network = true"
  type        = string
  default     = "pii-guard-net"
}

variable "subnet_name" {
  description = "Имя создаваемой подсети при create_network = true"
  type        = string
  default     = "pii-guard-subnet"
}

variable "subnet_cidr" {
  description = "Адресный блок создаваемой подсети"
  type        = string
  default     = "10.130.0.0/24"
}

variable "internal_cidr" {
  description = "Блок адресов, внутри которого трафик стенда разрешён целиком"
  type        = string
  default     = "10.128.0.0/16"
}

// --- Доступ ---

variable "ssh_public_key" {
  description = "Открытый ключ для доступа по SSH"
  type        = string
  default     = ""
}

// --- Машина сервиса ---

variable "app_static_ip" {
  description = "Зарезервированный внешний адрес машины сервиса. Он переживает перезапуск машины, поэтому адрес стенда не меняется."
  type        = string
  default     = "84.201.166.35"
}

variable "app_cores" {
  description = "Число ядер машины сервиса"
  type        = number
  default     = 24
}

variable "app_memory_gb" {
  description = "Память машины сервиса в гигабайтах"
  type        = number
  default     = 48
}

variable "app_http_port" {
  description = "Порт контракта проверки по HTTP"
  type        = number
  default     = 80
}

variable "app_https_port" {
  description = "Порт контракта проверки по HTTPS"
  type        = number
  default     = 443
}

// --- Машина генератора нагрузки ---

variable "load_cores" {
  description = "Число ядер машины генератора нагрузки"
  type        = number
  default     = 4
}

variable "load_memory_gb" {
  description = "Память машины генератора нагрузки в гигабайтах"
  type        = number
  default     = 8
}

variable "load_corpus_size" {
  description = "Размер набора данных, который генератор готовит при первом запуске"
  type        = number
  default     = 8000
}

// --- Откуда берётся сервис на машине ---

variable "app_binary_url" {
  description = "Ссылка на готовый исполняемый файл сервиса под linux/amd64. Самый быстрый путь: машина поднимается за минуту без сборки."
  type        = string
  default     = ""
}

variable "app_source_url" {
  description = "Ссылка на архив с исходным кодом (tar.gz или zip). Используется, если не задан app_binary_url."
  type        = string
  default     = ""
}

variable "app_repo_url" {
  description = "Ссылка на репозиторий с исходным кодом. Используется, если не заданы app_binary_url и app_source_url."
  type        = string
  default     = ""
}

variable "app_repo_ref" {
  description = "Ветка или метка репозитория"
  type        = string
  default     = "main"
}

variable "go_version" {
  description = "Версия Go для сборки на машине"
  type        = string
  default     = "1.26.8"
}

variable "pii_store_key" {
  description = "Ключ шифрования хранилища соответствий, 32 байта в кодировке base64. Пустое значение означает, что ключ сгенерируется на машине при первом запуске. Передавайте через TF_VAR_pii_store_key, в репозиторий ключ не кладём."
  type        = string
  sensitive   = true
  default     = ""
}

// --- Наблюдение ---

variable "enable_monitoring_on_app" {
  description = "Развернуть Prometheus, Grafana и сбор показателей машины прямо на машине сервиса"
  type        = bool
  default     = true
}

variable "enable_monitor_vm" {
  description = "Вынести наблюдение на отдельную машину. Нужно в режиме масштабирования: копии сервиса приходят и уходят, история показателей должна их пережить."
  type        = bool
  default     = false
}

variable "monitor_cores" {
  description = "Число ядер машины наблюдения"
  type        = number
  default     = 2
}

variable "monitor_memory_gb" {
  description = "Память машины наблюдения в гигабайтах"
  type        = number
  default     = 4
}

variable "grafana_port" {
  description = "Порт панели показателей. Открыт наружу группой безопасности."
  type        = number
  default     = 3000
}

variable "metrics_retention" {
  description = "Срок хранения показателей в Prometheus"
  type        = string
  default     = "72h"
}

variable "monitor_extra_targets" {
  description = "Дополнительные цели сбора показателей для машины наблюдения, в виде адрес:порт"
  type        = list(string)
  default     = []
}

// --- Общее хранилище соответствий ---

variable "enable_shared_store" {
  description = "Поднять общее хранилище соответствий. Без него горизонтальный рост невозможен: обратное преобразование не найдёт запись, созданную другой копией сервиса."
  type        = bool
  default     = false
}

variable "shared_store_kind" {
  description = "Вид общего хранилища: managed это управляемый кластер Redis, vm это одна машина с Redis в контейнере"
  type        = string
  default     = "vm"

  validation {
    condition     = contains(["managed", "vm"], var.shared_store_kind)
    error_message = "Допустимые значения: managed или vm."
  }
}

variable "redis_password" {
  description = "Пароль общего хранилища, от восьми символов. Передавайте через TF_VAR_redis_password."
  type        = string
  sensitive   = true
  default     = ""
}

variable "redis_port" {
  description = "Порт общего хранилища"
  type        = number
  default     = 6379
}

variable "redis_version" {
  description = "Версия Redis в управляемом кластере"
  type        = string
  default     = "7.2"
}

variable "redis_resource_preset" {
  description = "Класс хостов управляемого кластера Redis"
  type        = string
  default     = "hm3-c2-m8"
}

variable "redis_disk_gb" {
  description = "Размер диска хоста управляемого кластера Redis в гигабайтах"
  type        = number
  default     = 16
}

variable "redis_vm_cores" {
  description = "Число ядер машины с Redis в контейнере"
  type        = number
  default     = 4
}

variable "redis_vm_memory_gb" {
  description = "Память машины с Redis в контейнере в гигабайтах"
  type        = number
  default     = 8
}

variable "redis_maxmemory_mb" {
  description = "Верхняя граница памяти под записи в Redis на машине. Ниже объёма машины: остальное нужно системе и накладным расходам."
  type        = number
  default     = 6144
}

// --- Масштабирование ---

variable "enable_scaling" {
  description = "Включить группу машин с автоматическим масштабированием и сетевым балансировщиком. Требует enable_shared_store = true, проверяется предусловием в scaling.tf."
  type        = bool
  default     = false
}

variable "scaling_min" {
  description = "Минимальное число копий сервиса в группе"
  type        = number
  default     = 2

  validation {
    condition     = var.scaling_min >= 1
    error_message = "В группе должна оставаться хотя бы одна копия."
  }
}

variable "scaling_max" {
  description = "Максимальное число копий сервиса в группе"
  type        = number
  default     = 6
}

variable "scaling_cpu_target" {
  description = "Целевая загрузка процессора в процентах, при превышении группа добавляет копии"
  type        = number
  default     = 60

  validation {
    condition     = var.scaling_cpu_target > 0 && var.scaling_cpu_target <= 100
    error_message = "Порог загрузки задаётся в процентах, от 1 до 100."
  }
}

variable "scaling_cores" {
  description = "Число ядер одной копии сервиса в группе. Меньше, чем у одиночной машины: смысл группы в числе копий, а не в размере каждой."
  type        = number
  default     = 8
}

variable "scaling_memory_gb" {
  description = "Память одной копии сервиса в группе в гигабайтах"
  type        = number
  default     = 16
}

variable "scaling_service_account_id" {
  description = "Сервисный аккаунт, от имени которого группа создаёт машины. Нужны роли compute.editor, vpc.admin и load-balancer.admin."
  type        = string
  default     = ""
}
