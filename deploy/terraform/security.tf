// Группа безопасности стенда.
//
// Имя ресурса (yandex_vpc_security_group.stand) менять нельзя: группа заведена
// в облаке и сверена импортом. Набор правил повторяет то, что реально открыто.
//
// Наружу открыты только порты, нужные проверяющей системе и жюри: SSH для
// обслуживания, 80 и 443 для контракта проверки, порт панели показателей.
// Внутренний трафик между машинами стенда разрешён целиком: генератор нагрузки
// обращается к сервису по внутреннему адресу, минуя внешний, а общее хранилище
// и панель наблюдения ходят к сервису тем же путём.
resource "yandex_vpc_security_group" "stand" {
  name        = "pii-guard-sg"
  description = "Стенд модуля безопасности персональных данных"
  network_id  = local.network_id

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
    v4_cidr_blocks = [local.internal_cidr]
  }

  egress {
    description    = "Исходящие: обновления, реестр образов, языковая модель"
    protocol       = "ANY"
    from_port      = 0
    to_port        = 65535
    v4_cidr_blocks = ["0.0.0.0/0"]
  }
}

// Отдельная группа для проверок балансировщика. Сетевой балансировщик Yandex
// Cloud ходит на машины с адресов 198.18.235.0/24 и 198.18.248.0/24, без этого
// правила проверка живости не проходит и группа машин остаётся без трафика.
// Группа создаётся только вместе с масштабированием, чтобы не трогать стенд,
// который уже сверен с облаком.
resource "yandex_vpc_security_group" "balancer" {
  count = var.enable_scaling ? 1 : 0

  name        = "pii-guard-lb-sg"
  description = "Проверки живости от сетевого балансировщика"
  network_id  = local.network_id

  ingress {
    description    = "Проверки живости балансировщика"
    protocol       = "TCP"
    port           = var.app_http_port
    v4_cidr_blocks = ["198.18.235.0/24", "198.18.248.0/24"]
  }
}
