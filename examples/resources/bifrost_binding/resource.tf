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
