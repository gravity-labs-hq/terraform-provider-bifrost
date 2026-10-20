resource "bifrost_scaling" "web" {
  app         = "tienda"
  environment = "prod"
  workload    = "web"
  horizontal = {
    mode       = "auto"
    min        = 2
    max        = 6
    cpu_target = 70
  }
}
