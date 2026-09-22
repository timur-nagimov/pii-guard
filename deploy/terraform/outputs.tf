// Что выдаётся человеку после применения: адреса, панель, готовые команды.

locals {
  // Внешний адрес, на который шлют запросы. В режиме масштабирования это
  // балансировщик, иначе машина сервиса.
  balancer_ip = try(yandex_lb_network_load_balancer.app[0].listener[*].external_address_spec[0].address[0], "")
  entry_ip    = local.balancer_ip != "" ? local.balancer_ip : var.app_static_ip

  // Панель показателей: на отдельной машине наблюдения, если она поднята.
  panel_ip = var.enable_monitor_vm ? try(yandex_compute_instance.monitor[0].network_interface.0.nat_ip_address, "") : var.app_static_ip
}

output "app_url_http" {
  description = "Адрес сервиса для проверяющей системы"
  value       = "http://${local.entry_ip}/process"
}

output "app_url_https" {
  description = "Тот же контракт по защищённому протоколу, сертификат самоподписанный"
  value       = "https://${local.entry_ip}/process"
}

output "app_test_command" {
  description = "Проверить стенд одним запросом"
  value       = "curl -sS -X POST http://${local.entry_ip}/process -H 'Content-Type: application/json' -d '{\"id\":\"probe-1\",\"text\":\"Иван Петров, телефон +7 916 123-45-67\",\"direction\":\"mask\"}'"
}

output "app_external_ip" {
  description = "Внешний адрес машины сервиса"
  value       = var.app_static_ip
}

output "app_internal_ip" {
  description = "Внутренний адрес сервиса: по нему обращается генератор нагрузки"
  value       = yandex_compute_instance.app.network_interface.0.ip_address
}

output "app_metrics_url" {
  description = "Показатели сервиса в сыром виде"
  value       = "http://${var.app_static_ip}/metrics"
}

output "load_external_ip" {
  description = "Внешний адрес машины генератора нагрузки"
  value       = yandex_compute_instance.load.network_interface.0.nat_ip_address
}

output "load_internal_ip" {
  description = "Внутренний адрес машины генератора нагрузки"
  value       = yandex_compute_instance.load.network_interface.0.ip_address
}

output "grafana_url" {
  description = "Панель показателей для жюри, вход без пароля"
  value       = local.panel_ip == "" ? "панель не развёрнута" : "http://${local.panel_ip}:${var.grafana_port}"
}

output "prometheus_url" {
  description = "Prometheus доступен только с самой машины наблюдения: ssh -L 9090:127.0.0.1:9090"
  value       = "http://127.0.0.1:9090"
}

output "monitor_external_ip" {
  description = "Внешний адрес отдельной машины наблюдения, если она поднята"
  value       = try(yandex_compute_instance.monitor[0].network_interface.0.nat_ip_address, "")
}

output "shared_store_endpoint" {
  description = "Адрес общего хранилища соответствий, если оно включено"
  value       = local.shared_store_addr == "" ? "хранилище выключено, сервис работает в памяти процесса" : local.shared_store_addr
}

output "balancer_url" {
  description = "Адрес балансировщика перед группой машин, если масштабирование включено"
  value       = local.balancer_ip == "" ? "масштабирование выключено" : "http://${local.balancer_ip}/process"
}

output "loadgen_command" {
  description = "Нагрузочный прогон по внутреннему адресу: профиль проверяющей системы"
  value       = "ssh ubuntu@${yandex_compute_instance.load.network_interface.0.nat_ip_address} 'RPS=1000 DURATION=5m /usr/local/bin/pii-load.sh'"
}

output "loadgen_ceiling_command" {
  description = "Поиск потолка: без ограничения частоты, короткие тексты"
  value       = "ssh ubuntu@${yandex_compute_instance.load.network_interface.0.nat_ip_address} 'RPS=0 DURATION=2m /usr/local/bin/pii-load.sh -payload 250'"
}

output "ssh_commands" {
  description = "Доступ к машинам стенда"
  value = merge(
    {
      app  = "ssh ubuntu@${var.app_static_ip}"
      load = "ssh ubuntu@${yandex_compute_instance.load.network_interface.0.nat_ip_address}"
    },
    var.enable_monitor_vm ? { monitor = "ssh ubuntu@${try(yandex_compute_instance.monitor[0].network_interface.0.nat_ip_address, "")}" } : {},
    local.shared_store_vm ? { store = "ssh ubuntu@${try(yandex_compute_instance.redis[0].network_interface.0.nat_ip_address, "")}" } : {},
  )
}

output "stand_summary" {
  description = "Состав стенда одной строкой на каждый включённый кусок"
  value = {
    entry_point   = "http://${local.entry_ip}"
    panel         = local.panel_ip == "" ? "нет" : "http://${local.panel_ip}:${var.grafana_port}"
    shared_store  = local.shared_store_enabled ? var.shared_store_kind : "выключено"
    scaling       = var.enable_scaling ? "${var.scaling_min}..${var.scaling_max} копий, порог ${var.scaling_cpu_target} процентов" : "выключено"
    monitoring_on = var.enable_monitor_vm ? "отдельная машина" : (local.monitoring_on_app ? "машина сервиса" : "нет")
  }
}
