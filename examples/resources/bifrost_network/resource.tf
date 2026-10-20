resource "bifrost_network" "prod" {
  app            = bifrost_app.tienda.name
  environment    = "prod"
  domains        = ["www.tienda.example.org"]
  internet       = true
  internet_ports = [443]
}

# O solo destinos concretos:
resource "bifrost_network" "dev" {
  app         = bifrost_app.tienda.name
  environment = "dev"
  egress = [
    { cidr = "10.0.0.0/24", port = 5432, reason = "base externa" },
  ]
}
