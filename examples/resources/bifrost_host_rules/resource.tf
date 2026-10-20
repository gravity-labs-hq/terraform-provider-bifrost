# PUT reemplaza TODAS las reglas del host: el provider las envía completas, incluidas las nuevas.
resource "bifrost_host_rules" "web" {
  app              = bifrost_app.tienda.name
  environment      = "prod"
  host             = "www.tienda.example.org"
  security_headers = true
  compress         = true
  https_redirect   = true # quien entra por http:// pasa a https://
  timeout_seconds  = 60   # 504 si la App tarda más en responder las cabeceras
  rate_limit       = { average = 100, period_seconds = 1, burst = 50 }
  cors             = { origins = ["https://tienda.example.org"], methods = ["GET"] }

  # Las IP van en forma canónica (con máscara): así coincide con lo que guarda el servidor.
  ip_deny  = ["203.0.113.0/24", "198.51.100.7/32"]
  ip_notes = { "203.0.113.0/24" = "scanner conocido" }

  # Página de mantenimiento: quitar el bloque la apaga.
  maintenance = {
    html                = "<h1>Volvemos en unos minutos</h1>"
    retry_after_seconds = 600
    allow_ips           = ["192.0.2.10/32"] # el equipo que verifica la App
  }
}
