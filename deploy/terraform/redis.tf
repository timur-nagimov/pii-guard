// Общее хранилище соответствий.
//
// Соответствие «маска, исходный текст» по умолчанию живёт в памяти процесса.
// Это самый быстрый вариант и он полностью закрывает одномашинный стенд, но
// он же запрещает горизонтальный рост: обратное преобразование, пришедшее на
// вторую копию сервиса, не найдёт запись, созданную первой.
//
// Описаны оба способа снять это ограничение, переключение переменной
// shared_store_kind:
//
//   managed: управляемый кластер Redis. Облако само держит его живым,
//            переживает отказ хоста, обновляется без нас. Минимальная
//            конфигурация hm3-c2-m8, стоит заметно дороже машины, оценка в
//            README рядом.
//   vm:      одна машина с Redis в контейнере. Дешевле и поднимается за
//            минуту, но отказ машины означает потерю записей. Для стенда
//            этого достаточно: срок жизни записи и так час.
//
// По умолчанию хранилище выключено. Включается enable_shared_store = true, и
// тогда адрес хранилища автоматически попадает в настройки сервиса, см.
// local.app_config в compute.tf.

locals {
  shared_store_enabled = var.enable_shared_store
  shared_store_managed = var.enable_shared_store && var.shared_store_kind == "managed"
  shared_store_vm      = var.enable_shared_store && var.shared_store_kind == "vm"

  shared_store_host = local.shared_store_managed ? try(yandex_mdb_redis_cluster.shared[0].host[0].fqdn, "") : (
    local.shared_store_vm ? try(yandex_compute_instance.redis[0].network_interface.0.ip_address, "") : ""
  )

  shared_store_addr = local.shared_store_enabled ? "${local.shared_store_host}:${var.redis_port}" : ""

  // Пароль выбирается индексом по словарю по той же причине, что и ключ
  // шифрования в compute.tf: чтобы выключенное хранилище не прятало план.
  shared_store_password = { set = var.redis_password, unset = "" }[local.shared_store_enabled ? "set" : "unset"]

  // Пароль нужен обоим вариантам. Проверка снимается с секрета осознанно: в
  // сообщении об ошибке появляется только факт, что пароль слишком короткий.
  redis_password_ok = nonsensitive(length(var.redis_password) >= 8)
}

// Вариант «управляемый кластер»: минимальная конфигурация, один хост в той же
// сети и зоне, что и сервис. Хождение по сети внутри одной зоны добавляет к
// запросу десятые доли миллисекунды, это заметно на фоне текущих 0.97 мс доли
// 99, поэтому хранилище держится рядом, а не в другой зоне.
resource "yandex_mdb_redis_cluster" "shared" {
  count = local.shared_store_managed ? 1 : 0

  name                = "pii-guard-store"
  description         = "Общее хранилище соответствий модуля маскирования"
  environment         = "PRESTABLE"
  network_id          = local.network_id
  security_group_ids  = [yandex_vpc_security_group.stand.id]
  deletion_protection = false

  config {
    password         = var.redis_password
    version          = var.redis_version
    maxmemory_policy = "ALLKEYS_LRU"
  }

  resources {
    resource_preset_id = var.redis_resource_preset
    disk_size          = var.redis_disk_gb
    disk_type_id       = "network-ssd"
  }

  host {
    zone      = var.zone
    subnet_id = local.subnet_id
  }

  lifecycle {
    precondition {
      condition     = local.redis_password_ok
      error_message = "Задайте пароль хранилища: TF_VAR_redis_password, не короче восьми символов."
    }
  }
}

// Вариант «обычная машина»: Redis в контейнере, поднимается облачной
// инициализацией.
//
// Внешний адрес машине нужен: без него не скачать образ контейнера, а
// отдельного шлюза для исходящего трафика в стенде нет. Снаружи хранилище всё
// равно недоступно, группа безопасности не открывает порт 6379 никому, кроме
// самого стенда.
resource "yandex_compute_instance" "redis" {
  count = local.shared_store_vm ? 1 : 0

  name        = "pii-guard-store"
  hostname    = "pii-guard-store"
  platform_id = "standard-v3"
  zone        = var.zone

  resources {
    cores         = var.redis_vm_cores
    memory        = var.redis_vm_memory_gb
    core_fraction = 100
  }

  boot_disk {
    initialize_params {
      name     = "pii-guard-store-boot"
      image_id = data.yandex_compute_image.ubuntu.id
      type     = "network-ssd"
      size     = 30
    }
  }

  network_interface {
    subnet_id          = local.subnet_id
    nat                = true
    security_group_ids = [yandex_vpc_security_group.stand.id]
  }

  metadata = {
    ssh-keys = "ubuntu:${var.ssh_public_key}"
    user-data = templatefile("${local.cloud_init_dir}/redis.yaml.tftpl", {
      sysctl_b64 = filebase64("${local.cloud_init_dir}/sysctl-highload.conf")
      compose_b64 = base64encode(templatefile("${local.cloud_init_dir}/redis-compose.yml.tftpl", {
        password     = var.redis_password
        port         = var.redis_port
        maxmemory_mb = var.redis_maxmemory_mb
      }))
    })
  }

  lifecycle {
    precondition {
      condition     = local.redis_password_ok
      error_message = "Задайте пароль хранилища: TF_VAR_redis_password, не короче восьми символов."
    }
  }
}
