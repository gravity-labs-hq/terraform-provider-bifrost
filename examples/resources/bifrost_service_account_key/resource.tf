# Rotación: cambiar `keepers` genera otra clave (las anteriores vencen en 24 h).
resource "bifrost_service_account_key" "deploy" {
  app     = bifrost_app.tienda.name
  account = bifrost_service_account.deploy.name
  keepers = { rotacion = "2026-10" }
}

# Una segunda llave SIN tocar las anteriores (dos pipelines a la vez), que se revoca al destruir el recurso.
resource "bifrost_service_account_key" "extra" {
  app               = bifrost_app.tienda.name
  account           = bifrost_service_account.deploy.name
  keep_previous     = true
  revoke_on_destroy = true
}
