output "app_url_http" {
  description = "Адрес сервиса для проверяющей системы"
  value       = "http://${var.app_static_ip}/process"
}

output "app_url_https" {
  description = "Тот же контракт по защищённому протоколу, сертификат самоподписанный"
  value       = "https://${var.app_static_ip}/process"
}

output "app_internal_ip" {
  description = "Внутренний адрес сервиса: по нему обращается генератор нагрузки"
  value       = yandex_compute_instance.app.network_interface.0.ip_address
}

output "load_external_ip" {
  description = "Внешний адрес машины генератора нагрузки"
  value       = yandex_compute_instance.load.network_interface.0.nat_ip_address
}

output "grafana_url" {
  description = "Показатели для жюри"
  value       = "http://${var.app_static_ip}:3000"
}
