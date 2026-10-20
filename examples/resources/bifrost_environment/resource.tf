resource "bifrost_environment" "dev" {
  app  = bifrost_app.tienda.name
  name = "dev"
  quota = {
    cpu    = "1"
    memory = "2Gi"
    pods   = 10
    pvcs   = 2
  }
}

# El entorno que la API crea junto con la App se adopta:
resource "bifrost_environment" "prod" {
  app            = bifrost_app.tienda.name
  name           = "prod"
  adopt_existing = true
}
