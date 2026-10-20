# La App «pagos» puede llamar al servicio «api» (puerto 8080) de «tienda» en prod.
resource "bifrost_access" "pagos" {
  app         = "tienda"
  environment = "prod"
  from        = "pagos"
  service     = "api"
  port        = 8080
}
