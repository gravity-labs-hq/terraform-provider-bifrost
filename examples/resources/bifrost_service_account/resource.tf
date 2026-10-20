resource "bifrost_service_account" "deploy" {
  app          = bifrost_app.tienda.name
  name         = "deploy"
  role         = "deploy"
  environments = ["prod"]
}

# La clave inicial queda SENSIBLE en el state:
output "clave" {
  value     = bifrost_service_account.deploy.key_json
  sensitive = true
}
