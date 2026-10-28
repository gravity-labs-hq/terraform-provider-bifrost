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

# Control total de ESTA App (red, cuota, cuentas, vínculos, miembros…) y de nada más. Sin `environments`.
resource "bifrost_service_account" "manager" {
  app  = bifrost_app.tienda.name
  name = "manager"
  role = "manager"
}
