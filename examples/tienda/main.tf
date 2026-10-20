# Ejemplo completo «tienda»: una App con dos entornos, Postgres y Redis, una cuenta de servicio, el vínculo con GitLab,
# miembros, cuota, dominio propio, cola y pool de runners.
#
# Autenticación por variables de entorno (ver ../provider/provider.tf). Tres formas de correrlo:
#   - persona administradora:  BIFROST_API + BIFROST_ID_TOKEN_ENV (+ BIFROST_ID_TOKEN_GRANT=user)  o  BIFROST_TOKEN
#   - cuenta de servicio de PLATAFORMA (recomendado en CI, sin ser administrador): la crea una persona administradora una vez con ../bootstrap;
#     luego BIFROST_KEY_JSON (la clave) o BIFROST_ID_TOKEN_ENV + BIFROST_FEDERATED_PLATFORM=terraform (federación del job de GitLab).
#     Esta cuenta solo toca Apps/colas/pools cuyo nombre empiece con su `app_prefix` («tienda»).

terraform {
  required_providers {
    bifrost = {
      source = "gravity-labs-hq/bifrost"
    }
  }
}

provider "bifrost" {}

variable "dominio" {
  type    = string
  default = "www.tienda.example.org"
}

# --- La App ---------------------------------------------------------------------------------------------------------------------
resource "bifrost_app" "tienda" {
  name        = "tienda"
  description = "Tienda online"
  owners      = ["dueña@example.org", "socio@example.org"] # obligatorio con cuenta de plataforma

  # La API exige al menos un entorno al crear la App y lo crea ella: se adopta abajo.
  first_environment = "prod"
}

# Cuota por omisión de los entornos que se creen DESPUÉS (no cambia los existentes).
resource "bifrost_quota" "tienda" {
  app    = bifrost_app.tienda.name
  cpu    = "2"
  memory = "4Gi"
  pods   = 20
  pvcs   = 5
}

# --- Entornos -------------------------------------------------------------------------------------------------------------------
resource "bifrost_environment" "prod" {
  app            = bifrost_app.tienda.name
  name           = "prod"
  adopt_existing = true # es el que creó la App
}

resource "bifrost_environment" "dev" {
  app  = bifrost_app.tienda.name
  name = "dev"
  quota = { # la cuota de un entorno no se puede cambiar después
    cpu    = "1"
    memory = "2Gi"
    pods   = 10
    pvcs   = 2
  }
  depends_on = [bifrost_quota.tienda]
}

# --- Herramientas (Postgres y Redis) ----------------------------------------------------------------------------------------------
# Quitar una herramienta borra sus datos: por eso `confirm_data_loss` empieza en false y hay que ponerlo en true (y aplicar) antes de destruirla.
resource "bifrost_service" "postgres_prod" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  service     = "postgres" # ver el catálogo y sus opciones con `bifrost services describe postgres`
}

resource "bifrost_service" "postgres_dev" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.dev.name
  service     = "postgres"
}

resource "bifrost_service" "redis_prod" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  service     = "redis"
}

# Datos de conexión NO secretos (host, puerto...); las contraseñas viven solo en el Secret de Kubernetes indicado.
data "bifrost_service" "postgres_prod" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  service     = bifrost_service.postgres_prod.service
}

# --- Identidades ----------------------------------------------------------------------------------------------------------------
# Cuenta de servicio para un pipeline que despliega solo en prod. Su clave privada queda SENSIBLE en el state.
resource "bifrost_service_account" "deploy" {
  app          = bifrost_app.tienda.name
  name         = "deploy"
  role         = "deploy"
  environments = [bifrost_environment.prod.name]
}

# Vínculo de federación: el repo de GitLab despliega sin secretos guardados, solo desde ramas protegidas y declarando environment: production.
resource "bifrost_binding" "gitlab" {
  app           = bifrost_app.tienda.name
  project_path  = "acme/tienda"
  role          = "deploy"
  ref_protected = true
  claims        = { environment = "production" }
  environments  = [bifrost_environment.prod.name]
}

resource "bifrost_member" "equipo" {
  for_each = { "luis@example.org" = "deploy", "marta@example.org" = "readonly" }
  app      = bifrost_app.tienda.name
  email    = each.key
  role     = each.value
}

# --- Red: dominio propio y salida a internet solo por HTTPS ------------------------------------------------------------------------
resource "bifrost_network" "prod" {
  app            = bifrost_app.tienda.name
  environment    = bifrost_environment.prod.name
  domains        = [var.dominio]
  internet       = true
  internet_ports = [443]
}

resource "bifrost_host_rules" "web" {
  app              = bifrost_app.tienda.name
  environment      = bifrost_environment.prod.name
  host             = var.dominio
  security_headers = true
  compress         = true
  rate_limit       = { average = 100, period_seconds = 1, burst = 50 }
  depends_on       = [bifrost_network.prod]
}

# --- Colas y runners (solo administradores) -------------------------------------------------------------------------------------
resource "bifrost_queue" "correos" {
  name    = "tienda-correos" # el prefijo de la cuenta de plataforma
  display = "Correos transaccionales"
}

resource "bifrost_runner_pool" "ci" {
  name    = "tienda-ci"
  labels  = ["docker"]
  origins = ["own"]
  max     = 4
  app     = bifrost_app.tienda.name
}

output "clave_deploy" {
  value     = bifrost_service_account.deploy.key_json
  sensitive = true
}

output "conexion_postgres_prod" {
  value     = data.bifrost_service.postgres_prod.public_values
  sensitive = true
}
