resource "bifrost_service" "postgres" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  service     = "postgres"

  # Para destruirlo (borra los datos) primero pon true y aplica:
  confirm_data_loss = false
}
