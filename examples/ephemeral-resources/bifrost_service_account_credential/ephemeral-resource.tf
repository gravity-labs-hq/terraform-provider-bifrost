# La clave nueva NO entra al state ni al plan. Rota la clave en cada plan/apply: úsala con vigencia corta y pásala a un atributo write-only.
ephemeral "bifrost_service_account_credential" "ci" {
  app             = "tienda"
  account         = "deploy"
  expires_in_days = 1
}

# Ejemplo (otro provider): resource "gitlab_project_variable" "k" { value_wo = ephemeral.bifrost_service_account_credential.ci.key_json ... }
