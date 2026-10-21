# Puertos públicos TCP/UDP de UN entorno: correo de un servidor Stalwart (SMTP 25/587, IMAP 143/993).
#
# ADVERTENCIA DE SEGURIDAD: abrir un puerto expone el servicio en la IP del servidor, FUERA de Cloudflare (que solo hace proxy HTTP/S).
# Sin `allow_cidrs` cualquiera en internet llega al puerto; un SMTP mal configurado es un relay abierto. Restringe por rango siempre que puedas
# (sobre todo en UDP). El recurso es AUTORITATIVO: reemplaza todos los puertos del entorno y destruirlo los cierra todos.
# El workload (`stalwart`) debe existir en el entorno y tener un selector `matchLabels` que ningún otro workload cumpla.
resource "bifrost_ports" "prod" {
  app         = bifrost_app.correo.name
  environment = "prod"

  ports = {
    smtp       = { protocol = "tcp", port = 25, target = { kind = "deployment", name = "stalwart", port = 25 } }
    submission = { protocol = "tcp", port = 587, target = { kind = "deployment", name = "stalwart", port = 587 } }
    imap       = { protocol = "tcp", port = 143, target = { kind = "deployment", name = "stalwart", port = 143 } }
    # Solo desde la oficina (forma CIDR canónica; sin rangos internos ni /0).
    imaps = {
      protocol    = "tcp"
      port        = 993
      target      = { kind = "deployment", name = "stalwart", port = 993 }
      allow_cidrs = ["203.0.113.0/24", "198.51.100.7/32"]
    }
  }
}
