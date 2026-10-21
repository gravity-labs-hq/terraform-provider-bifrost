# Salida a internet por puerto ACOTADA A UN WORKLOAD: el servidor de correo Stalwart entrega SMTP (25) a otros dominios; el resto del entorno no.
#
# ADVERTENCIA DE SEGURIDAD: `internet_ports` de bifrost_network vale para TODOS los pods del entorno. Abrir el 25 ahí deja que cualquier pod comprometido
# (api, worker, un SSRF) envíe SMTP directo desde la IP del servidor y pase el SPF. Mantén el 25 CERRADO en el entorno y ábrelo solo para el StatefulSet de
# correo con esta regla. El recurso es AUTORITATIVO: reemplaza todas las reglas del entorno y destruirlo las revoca. Nunca se abren 22, 23, 53, 135-139, 445,
# 3389, bases de datos ni los puertos del propio servidor. El workload debe existir y su selector `matchLabels` no puede cumplirlo otro workload.
resource "bifrost_egress" "prod" {
  app         = bifrost_app.inboxie.name
  environment = "prod"

  rules = {
    smtp-out = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "inboxie-stalwart" } }
  }
}
