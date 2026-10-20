# BOOTSTRAP (una sola vez, lo ejecuta una PERSONA ADMINISTRADORA): crea la cuenta de servicio de plataforma con la que Terraform corre después
# desde el CI SIN ser administrador. La cuenta solo tiene los scopes indicados, solo toca las Apps que empiezan con `app_prefix` (o las que cree),
# caduca y se rota. Ver ../tienda/main.tf para lo que se administra con ella.

terraform {
  required_providers {
    bifrost = {
      source = "gravity-labs-hq/bifrost"
    }
  }
}

# Autenticación de la persona administradora (ver ../provider/provider.tf): BIFROST_API + BIFROST_ID_TOKEN_ENV/BIFROST_ID_TOKEN_GRANT=user, o BIFROST_TOKEN.
provider "bifrost" {}

resource "bifrost_platform_account" "terraform" {
  name       = "terraform"
  app_prefix = "tienda" # Apps, colas, pools y enlaces deben empezar con este prefijo

  # Mínimos para el ejemplo «tienda». NUNCA hay scopes sobre administradores, personas ni cuentas de plataforma.
  scopes = [
    "apps:create", "apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage",
    "queues:manage", "runners:manage", "replication:propose",
  ]

  # Dominios de correo a los que la cuenta puede dar rol de owner o miembro (obligatorio con apps:create / members:manage).
  allowed_domains = ["example.org"]

  # OPT-IN: identities:manage (cuentas de servicio y vínculos de CI de las Apps permitidas) da acceso de despliegue; exige apps o app_prefix.
  allow_identities = true

  expires_in_days = 90                          # obligatoria (máx 365); cambiarla o cambiar `keepers` ROTA la clave
  keepers         = { rotacion = "2026-10" }

  # Opcional y recomendado: el job de GitLab asume la cuenta con su ID token (sin guardar la clave en el CI).
  ci_binding = {
    project_path = "acme/infra" # grupo/repo exacto
    environment  = "production" # el job debe declarar `environment: production`
  }
}

# La clave privada queda SENSIBLE en el state (protégelo). Entrégala al CI como variable de tipo archivo/enmascarada,
# o mejor aún, usa solo el ci_binding con `federated_platform = "terraform"`.
output "clave_ci" {
  value     = bifrost_platform_account.terraform.key_json
  sensitive = true
}
