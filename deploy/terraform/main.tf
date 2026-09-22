// Стенд сервиса маскирования персональных данных в Yandex Cloud.
//
// Файл описывает то, что реально поднято и работает: две машины, группу
// безопасности и статический адрес. Соответствие кода и облака проверяется
// командами terraform import и terraform plan, см. README.md рядом.
//
// Раздел с группой машин и автоматическим масштабированием вынесен в
// scaling.tf и по умолчанию выключен: смысл включать его появляется только
// после перевода хранилища соответствий в общее, см. комментарий там.

terraform {
  required_version = ">= 1.6"
  required_providers {
    yandex = {
      source  = "yandex-cloud/yandex"
      version = "~> 0.120"
    }
  }
}

provider "yandex" {
  cloud_id  = var.cloud_id
  folder_id = var.folder_id
  zone      = var.zone
}

// Сеть и подсеть созданы до стенда и используются как есть.
data "yandex_vpc_network" "default" {
  network_id = var.network_id
}

data "yandex_vpc_subnet" "default" {
  subnet_id = var.subnet_id
}

// Образ операционной системы для обеих машин.
data "yandex_compute_image" "ubuntu" {
  family = "ubuntu-2404-lts-oslogin"
}

// Группа безопасности стенда.
//
// Наружу открыты только те порты, которые нужны проверяющей системе и жюри.
// Внутренний трафик между машинами стенда разрешён целиком: генератор
// нагрузки обращается к сервису по внутреннему адресу, минуя внешний.
resource "yandex_vpc_security_group" "stand" {
  name        = "pii-guard-sg"
  description = "Стенд модуля безопасности персональных данных"
  network_id  = data.yandex_vpc_network.default.id

  ingress {
    description    = "Доступ по SSH"
    protocol       = "TCP"
    port           = 22
    v4_cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description    = "Контракт проверки по HTTP"
    protocol       = "TCP"
    port           = 80
    v4_cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description    = "Контракт проверки по HTTPS"
    protocol       = "TCP"
    port           = 443
    v4_cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description    = "Показатели для жюри"
    protocol       = "TCP"
    port           = 3000
    v4_cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description    = "Проверка доступности"
    protocol       = "ICMP"
    v4_cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description    = "Трафик внутри стенда"
    protocol       = "ANY"
    from_port      = 0
    to_port        = 65535
    v4_cidr_blocks = ["10.128.0.0/16"]
  }

  egress {
    description    = "Исходящие: обновления, реестр образов, языковая модель"
    protocol       = "ANY"
    from_port      = 0
    to_port        = 65535
    v4_cidr_blocks = ["0.0.0.0/0"]
  }
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
    subnet_id          = data.yandex_vpc_subnet.default.id
    nat                = true
    nat_ip_address     = var.app_static_ip
    security_group_ids = [yandex_vpc_security_group.stand.id]
  }

  metadata = {
    ssh-keys  = "ubuntu:${var.ssh_public_key}"
    user-data = file("${path.module}/cloud-init-app.yaml")
  }

  // Машину нельзя останавливать до письменного подтверждения организаторов,
  // что нагрузочный прогон состоялся: момент прогона заранее неизвестен.
  lifecycle {
    prevent_destroy = true
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
    subnet_id          = data.yandex_vpc_subnet.default.id
    nat                = true
    security_group_ids = [yandex_vpc_security_group.stand.id]
  }

  metadata = {
    ssh-keys  = "ubuntu:${var.ssh_public_key}"
    user-data = file("${path.module}/cloud-init-load.yaml")
  }
}
