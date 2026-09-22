// Машины стенда: сервис и генератор нагрузки.
//
// Имена ресурсов yandex_compute_instance.app и yandex_compute_instance.load
// менять нельзя: машины заведены в облаке и сверены импортом.

data "yandex_compute_image" "ubuntu" {
  family = "ubuntu-2404-lts-oslogin"
}

locals {
  cloud_init_dir = "${path.module}/cloud-init"

  // Ключ шифрования хранилища. Проверка «задан ли ключ» снимается с секрета
  // осознанно: в план попадает только факт наличия, не значение.
  //
  // Значение выбирается индексом по словарю, а не тернарным оператором.
  // Причина: тернарный оператор помечает результат секретным даже когда
  // выбрана пустая ветка, и тогда весь план по машине сворачивается в
  // (sensitive value), то есть перестаёт быть читаемым. При выборе по ключу
  // метка переходит только с выбранного элемента, и план прячет настройки
  // машины ровно тогда, когда секрет действительно задан.
  store_key_set = nonsensitive(var.pii_store_key != "")
  app_store_key = { set = var.pii_store_key, unset = "" }[local.store_key_set ? "set" : "unset"]

  // Файл настроек берётся из репозитория и правится под стенд: на машине
  // сервис слушает 80 и 443, а в репозитории лежат порты для запуска на
  // рабочем компьютере. Один источник правды, правка ровно в двух строках.
  app_config_repo = file("${path.module}/../../configs/config.yaml")
  app_config_ports = replace(
    replace(local.app_config_repo, "/(?m)^  http: .*$/", "  http: \":${var.app_http_port}\""),
    "/(?m)^  https: .*$/", "  https: \":${var.app_https_port}\""
  )

  // При включённом общем хранилище его адрес попадает в настройки сервиса.
  app_config = local.shared_store_enabled ? replace(local.app_config_ports, "/(?m)^store:$/", join("\n", [
    "store:",
    "  redis:",
    "    addr: \"${local.shared_store_addr}\"",
    "    password: \"${local.shared_store_password}\"",
  ])) : local.app_config_ports

  // Настройки разворачивания для скрипта на машине.
  deploy_env_app = join("\n", [
    "# Настройки разворачивания сервиса, читает /usr/local/bin/pii-guard-deploy.sh",
    "APP_DIR=\"/opt/pii-guard\"",
    "HTTP_PORT=\"${var.app_http_port}\"",
    "BINARY_URL=\"${var.app_binary_url}\"",
    "SOURCE_URL=\"${var.app_source_url}\"",
    "REPO_URL=\"${var.app_repo_url}\"",
    "REPO_REF=\"${var.app_repo_ref}\"",
    "GO_VERSION=\"${var.go_version}\"",
    "STORE_KEY=\"${local.app_store_key}\"",
    "REDIS_ADDR=\"${local.shared_store_addr}\"",
    "REDIS_PASSWORD=\"${local.shared_store_password}\"",
    "",
  ])

  deploy_env_load = join("\n", [
    "# Настройки машины генератора нагрузки, читает /usr/local/bin/pii-load-deploy.sh",
    "LOAD_DIR=\"/opt/pii-load\"",
    "SOURCE_URL=\"${var.app_source_url}\"",
    "REPO_URL=\"${var.app_repo_url}\"",
    "REPO_REF=\"${var.app_repo_ref}\"",
    "GO_VERSION=\"${var.go_version}\"",
    "CORPUS_SIZE=\"${var.load_corpus_size}\"",
    "APP_URL=\"http://${yandex_compute_instance.app.network_interface.0.ip_address}\"",
    "",
  ])

  user_data_app = templatefile("${local.cloud_init_dir}/app.yaml.tftpl", {
    sysctl_b64        = filebase64("${local.cloud_init_dir}/sysctl-highload.conf")
    limits_b64        = filebase64("${local.cloud_init_dir}/limits-nofile.conf")
    deploy_env_b64    = base64encode(local.deploy_env_app)
    deploy_script_b64 = filebase64("${local.cloud_init_dir}/deploy-app.sh")
    config_b64        = base64encode(local.app_config)
    unit_b64          = filebase64("${local.cloud_init_dir}/pii-guard.service")
    monitoring        = local.monitoring_on_app
    monitoring_files  = local.monitoring_on_app ? local.monitoring_files_app : {}
  })

  user_data_load = templatefile("${local.cloud_init_dir}/load.yaml.tftpl", {
    sysctl_b64        = filebase64("${local.cloud_init_dir}/sysctl-highload.conf")
    limits_b64        = filebase64("${local.cloud_init_dir}/limits-nofile.conf")
    deploy_env_b64    = base64encode(local.deploy_env_load)
    deploy_script_b64 = filebase64("${local.cloud_init_dir}/deploy-load.sh")
    load_script_b64   = filebase64("${local.cloud_init_dir}/pii-load.sh")
  })
}

// Машина сервиса.
//
// Размер выбран по замеру: один проход детекторов стоит около одной целой
// двух десятых миллисекунды на килобайт текста на одном ядре, поэтому тысяча
// запросов в секунду по два килобайта занимает примерно два с половиной ядра
// из двадцати четырёх. Запас десятикратный, он же покрывает обработку
// текстов в сотни килобайт, которая идёт параллельно по кускам.
resource "yandex_compute_instance" "app" {
  name        = "pii-guard-app"
  hostname    = "pii-guard-app"
  platform_id = "standard-v3"
  zone        = var.zone

  resources {
    cores         = var.app_cores
    memory        = var.app_memory_gb
    core_fraction = 100
  }

  boot_disk {
    initialize_params {
      name     = "pii-guard-app-boot"
      image_id = data.yandex_compute_image.ubuntu.id
      type     = "network-ssd"
      size     = 60
    }
  }

  network_interface {
    subnet_id          = local.subnet_id
    nat                = true
    nat_ip_address     = var.app_static_ip
    security_group_ids = [yandex_vpc_security_group.stand.id]
  }

  metadata = {
    ssh-keys  = "ubuntu:${var.ssh_public_key}"
    user-data = local.user_data_app
  }

  lifecycle {
    // Машину нельзя останавливать до письменного подтверждения организаторов,
    // что нагрузочный прогон состоялся: момент прогона заранее неизвестен.
    prevent_destroy = true

    // Облачная инициализация передаётся через метаданные, а они ограничены
    // 256 КБ. Растут в них панели наблюдения, поэтому предел проверяется до
    // применения, а не выясняется отказом облака.
    precondition {
      condition     = local.monitoring_payload_bytes < 180000
      error_message = "Файлы наблюдения занимают ${local.monitoring_payload_bytes} байт: облачная инициализация не влезет в 256 КБ. Уберите лишние панели из deploy/grafana/dashboards либо поставьте enable_monitoring_on_app = false."
    }
  }
}

// Машина генератора нагрузки. Держится отдельно, чтобы замеры не искажались
// нагрузкой самого генератора на процессор сервиса.
resource "yandex_compute_instance" "load" {
  name        = "pii-guard-load"
  hostname    = "pii-guard-load"
  platform_id = "standard-v3"
  zone        = var.zone

  resources {
    cores         = var.load_cores
    memory        = var.load_memory_gb
    core_fraction = 100
  }

  boot_disk {
    initialize_params {
      name     = "pii-guard-load-boot"
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
    ssh-keys  = "ubuntu:${var.ssh_public_key}"
    user-data = local.user_data_load
  }
}
