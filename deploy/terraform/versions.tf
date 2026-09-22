// Требования к версиям и настройка провайдера.
//
// Модуль собран под OpenTofu (команда tofu) и официальный провайдер Yandex
// Cloud. Версия провайдера закреплена диапазоном: мажорные изменения схемы
// ресурсов группы машин и балансировщика ломают этот код молча.

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
