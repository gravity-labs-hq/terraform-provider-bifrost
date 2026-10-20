# Configuración del provider. Usa UNA sola forma de autenticación. Todos los valores admiten su variable de entorno BIFROST_*
# (el provider falla si encuentra más de una forma a la vez).

terraform {
  required_providers {
    bifrost = {
      source  = "gravity-labs-hq/bifrost"
      version = "~> 0.1"
    }
  }
}

# 1) Persona administradora con el ID token de tu proveedor de identidad (recomendado para gestionar la PLATAFORMA desde un CI propio:
#    crear Apps, redes, colas, runners, administradores). El ID token llega por variable de entorno y se canjea por un token de ~1 hora.
provider "bifrost" {
  api            = "https://plane.example.com" # o BIFROST_API
  id_token_env   = "BIFROST_ID_TOKEN"          # nombre de la variable que contiene el ID token
  id_token_grant = "user"
}

# 2) Federación del CI (GitLab, sin secretos guardados). Limitada a la App del vínculo: sirve para operar UNA App
#    (entornos, escalado...), no para administrar la plataforma. En GitLab:  id_tokens: { BIFROST_ID_TOKEN: { aud: https://plane.example.com } }
# provider "bifrost" {
#   api           = "https://plane.example.com"
#   id_token_env  = "BIFROST_ID_TOKEN"
#   federated_app = "tienda"
# }

# 2b) Federación del CI asumiendo una cuenta de servicio de PLATAFORMA (recomendado para Terraform en CI sin ser administrador):
#     la crea una persona administradora una vez (examples/bootstrap) y su vínculo exige proyecto exacto, rama protegida y environment.
# provider "bifrost" {
#   api                = "https://plane.example.com"
#   id_token_env       = "BIFROST_ID_TOKEN"
#   federated_platform = "terraform"
# }

# 3) Cuenta de servicio con clave (cualquier CI): una de plataforma (scopes acotados) o una de App (solo llega a su App).
# provider "bifrost" {
#   key_file = "/ruta/clave.json"   # o key_json (variable sensible) / BIFROST_KEY_FILE / BIFROST_KEY_JSON; la `api` sale de la clave
# }

# 4) Token ya emitido (p. ej. de `bifrost auth ...`); dura ~1 hora y no se renueva. Para pruebas.
# provider "bifrost" {
#   api   = "https://plane.example.com"
#   token = var.bifrost_token       # sensible; o BIFROST_TOKEN
# }

# TLS: se verifica SIEMPRE el certificado. Para una CA privada: ca_file = "/ruta/ca.pem" (o BIFROST_CA_FILE).
