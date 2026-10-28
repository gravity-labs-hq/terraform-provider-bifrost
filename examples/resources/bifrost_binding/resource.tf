resource "bifrost_binding" "gitlab" {
  app           = bifrost_app.tienda.name
  project_path  = "acme/tienda"
  role          = "deploy"
  ref_protected = true
  claims        = { environment = "production" }
  environments  = ["prod"]
  # Solo estas ramas pueden desplegar (nombres exactos o con un `*` final); vacío = cualquier ref.
  branches = ["main", "release/*"]
}

# Vínculo manager (CI con control total de la App): exige rama protegida, ramas exactas y la condición environment.
resource "bifrost_binding" "gitlab_manager" {
  app           = bifrost_app.tienda.name
  project_path  = "acme/tienda-infra"
  role          = "manager"
  ref_protected = true
  branches      = ["main"]
  claims        = { environment = "production" }
}
