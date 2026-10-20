resource "bifrost_member" "ana" {
  app   = bifrost_app.tienda.name
  email = "ana@example.org"
  role  = "deploy"
}
