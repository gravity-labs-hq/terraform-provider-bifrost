# Bootstrap: lo crea UNA persona administradora; el CI usa después la clave (o la federación) sin ser administrador.
resource "bifrost_platform_account" "terraform" {
  name            = "terraform"
  app_prefix      = "tienda"
  scopes          = ["apps:create", "apps:manage", "network:manage", "queues:manage"]
  expires_in_days = 90
  keepers         = { rotacion = "2026-10" }
  ci_binding = {
    project_path = "acme/infra"
    environment  = "production"
  }
}

# Cuenta que solo programa la carga de sus Apps (schedule:manage no incluye ni lo incluye ningún otro scope).
resource "bifrost_platform_account" "programadora" {
  name            = "programadora"
  app_prefix      = "tienda"
  scopes          = ["schedule:manage"]
  expires_in_days = 90
}
