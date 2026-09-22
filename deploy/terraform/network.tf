// Сеть стенда.
//
// Два режима. По умолчанию модуль берёт уже существующие сеть и подсеть: в них
// подняты обе машины, пересоздавать их нельзя. Для разворачивания с нуля в
// пустом каталоге достаточно поставить create_network = true, тогда сеть и
// подсеть создаёт сам модуль, а их идентификаторы подставляются автоматически.
//
// Адреса ресурсов существующих машин от этого не меняются: они ссылаются на
// local.subnet_id, значение которого в режиме по умолчанию совпадает с тем,
// что уже записано в состоянии.

resource "yandex_vpc_network" "stand" {
  count       = var.create_network ? 1 : 0
  name        = var.network_name
  description = "Сеть стенда модуля безопасности персональных данных"

  lifecycle {
    // Защита от случайного включения на работающем стенде. Создание своей
    // сети меняет сеть у группы безопасности, а это её пересоздание и простой
    // сервиса. Режим включается только осознанно, вместе с обнулением
    // идентификаторов существующей сети.
    precondition {
      condition     = var.network_id == "" && var.subnet_id == ""
      error_message = "Режим создания сети рассчитан на пустой каталог. Передайте -var network_id= -var subnet_id= вместе с create_network=true, иначе модуль предложит пересоздать группу безопасности работающего стенда."
    }
  }
}

resource "yandex_vpc_subnet" "stand" {
  count          = var.create_network ? 1 : 0
  name           = var.subnet_name
  description    = "Подсеть стенда в зоне ${var.zone}"
  zone           = var.zone
  network_id     = yandex_vpc_network.stand[0].id
  v4_cidr_blocks = [var.subnet_cidr]
}

data "yandex_vpc_network" "default" {
  count      = var.create_network ? 0 : 1
  network_id = var.network_id
}

data "yandex_vpc_subnet" "default" {
  count     = var.create_network ? 0 : 1
  subnet_id = var.subnet_id
}

locals {
  network_id = var.create_network ? yandex_vpc_network.stand[0].id : data.yandex_vpc_network.default[0].id
  subnet_id  = var.create_network ? yandex_vpc_subnet.stand[0].id : data.yandex_vpc_subnet.default[0].id

  // Подсеть, внутри которой трафик стенда разрешён целиком. В режиме создания
  // сети это ровно заданный блок, иначе весь диапазон зоны, как было настроено
  // при первом подъёме стенда.
  internal_cidr = var.create_network ? var.subnet_cidr : var.internal_cidr
}
