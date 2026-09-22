// Переменные стенда. Значения по умолчанию соответствуют тому, что поднято.

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

variable "network_id" {
  description = "Идентификатор существующей сети"
  type        = string
  default     = "enpc9oglebtvo74lcd4t"
}

variable "subnet_id" {
  description = "Идентификатор существующей подсети в выбранной зоне"
  type        = string
  default     = "e2l6qlb8r7c1q4prmu1e"
}

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

variable "ssh_public_key" {
  description = "Открытый ключ для доступа по SSH"
  type        = string
  default     = ""
}

variable "enable_scaling" {
  description = "Включить группу машин с автоматическим масштабированием и балансировщиком. Требует общего хранилища соответствий, см. scaling.tf."
  type        = bool
  default     = false
}

variable "scaling_min" {
  description = "Минимальное число машин в группе"
  type        = number
  default     = 2
}

variable "scaling_max" {
  description = "Максимальное число машин в группе"
  type        = number
  default     = 6
}

variable "scaling_service_account_id" {
  description = "Сервисный аккаунт, от имени которого группа создаёт машины"
  type        = string
  default     = ""
}
