resource "bifrost_quota" "tienda" {
  app    = bifrost_app.tienda.name
  cpu    = "2"
  memory = "4Gi"
  pods   = 20
  pvcs   = 5
}
