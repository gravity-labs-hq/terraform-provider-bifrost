resource "bifrost_app" "tienda" {
  name              = "tienda"
  description       = "Tienda online"
  group             = "ventas" # agrupa las Apps en «Personas y permisos» (opcional)
  owner             = "dueña@example.org"
  first_environment = "prod"
}
