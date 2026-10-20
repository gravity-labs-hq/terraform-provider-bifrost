data "bifrost_service" "postgres" {
  app         = "tienda"
  environment = "prod"
  service     = "postgres"
}

# host, puerto... (la contraseña vive solo en el Secret de Kubernetes data.bifrost_service.postgres.secret_name)
output "pg" {
  value     = data.bifrost_service.postgres.public_values
  sensitive = true
}
