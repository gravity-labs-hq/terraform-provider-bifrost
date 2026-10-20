resource "bifrost_runner_pool" "ci" {
  name    = "tienda-ci"
  labels  = ["docker"]
  origins = ["own"]
  max     = 4
  app     = "tienda"
}
