# terraform-provider-bifrost

Provider de Terraform y OpenTofu para **Bifröst**, la plataforma de despliegue de Apps. Administra como código: Apps, entornos, herramientas (Postgres, Redis…),
cuentas de servicio, miembros, vínculos de CI, cuotas, red y dominios, accesos entre Apps, escalado, colas, enlaces de replicación, pools de runners y
administradores, con `plan`/`apply`/`destroy`, `import` y detección de *drift*.

- Proyecto principal: <https://github.com/gravity-labs-hq/bifrost>
- Registry: `gravity-labs-hq/bifrost` (registry.terraform.io y registry.opentofu.org)
- Referencia de recursos: [docs/](docs/index.md) · Ejemplos: [examples/](examples/) (completo: [examples/tienda](examples/tienda/main.tf))
- Diseño y decisiones: [DESIGN.md](DESIGN.md) · Inventario de la API y brechas: [docs/inventario-api.md](docs/inventario-api.md)

## Uso

```hcl
terraform {
  required_providers {
    bifrost = {
      source  = "gravity-labs-hq/bifrost"
      version = "~> 0.1"
    }
  }
}

provider "bifrost" {
  api            = "https://plane.example.com"   # la URL de plane de TU instalación de Bifröst (o BIFROST_API)
  id_token_env   = "BIFROST_ID_TOKEN"            # nombre de la variable con el ID token de tu proveedor de identidad
  id_token_grant = "user"
}

resource "bifrost_app" "tienda" {
  name              = "tienda"
  description       = "Tienda online"
  owner             = "ana@example.com"
  first_environment = "prod"
}
```

## Requisitos

- Terraform u OpenTofu con protocolo de plugins 6 (Terraform >= 1.0). Probado con Terraform 1.15 y OpenTofu 1.13; el recurso efímero `bifrost_service_account_credential` exige Terraform >= 1.10 u OpenTofu >= 1.11.
- Una instalación de Bifröst accesible por `https` (la verificación TLS **no se puede desactivar**; para una CA privada, `ca_file`/`ca_pem`).
  El provider habla con **plane**, la API `/v1` de Bifröst (`https://plane.<tu-dominio>`).
- **Compatibilidad de versiones:** cada versión del provider se prueba contra la API de la versión de Bifröst con el mismo `MAJOR.MINOR` (el contrato está fijado por el servidor
  simulado `internal/fakeapi`). Una API más nueva suele funcionar; una más antigua puede carecer de endpoints (ver [docs/inventario-api.md](docs/inventario-api.md)).

## Autenticación (una sola forma a la vez)

Ver [examples/provider/provider.tf](examples/provider/provider.tf). Resumen:

| Forma | Alcance |
|---|---|
| `id_token_env` + `id_token_grant="user"` (persona administradora) | toda la plataforma |
| `id_token_env` + `federated_platform` (CI sin secretos guardados, cuenta de servicio de plataforma) | los scopes de la cuenta |
| `id_token_env` + `federated_app` (federación de CI) | solo la App del vínculo |
| `key_file` / `key_json` (cuenta de servicio) | su App, o los scopes de la cuenta de plataforma |
| `token` (Bearer ya emitido, dura ~1 h) | el de quien lo emitió |

Todo admite variable de entorno: `BIFROST_API`, `BIFROST_TOKEN`, `BIFROST_KEY_FILE`, `BIFROST_KEY_JSON`, `BIFROST_ID_TOKEN`, `BIFROST_ID_TOKEN_ENV`,
`BIFROST_ID_TOKEN_GRANT`, `BIFROST_FEDERATED_APP`, `BIFROST_FEDERATED_PLATFORM`, `BIFROST_CA_FILE`, `BIFROST_CA_PEM`.
Si se configuran varias formas a la vez, el provider falla en vez de elegir una.

### Terraform en CI sin ser administrador

Una persona administradora crea **una vez** la cuenta de servicio de plataforma ([examples/bootstrap](examples/bootstrap/main.tf)) con scopes mínimos y un `app_prefix`;
el CI la usa con `key_file`/`key_json` o, sin guardar secretos, con `id_token_env` + `federated_platform`. `bifrost_admin`, `bifrost_platform_account`,
purgar y restaurar siguen exigiendo persona administradora.

## Estado de los recursos

Completos (create/read/update/delete/import, probados con el servidor simulado): `bifrost_app`, `bifrost_environment`, `bifrost_service`,
`bifrost_service_account` (+`_key`), `bifrost_platform_account`, `bifrost_member`, `bifrost_binding`, `bifrost_quota`*, `bifrost_network`, `bifrost_host_rules`,
`bifrost_access`, `bifrost_scaling`*, `bifrost_schedule` (programación de carga; destruir la deja desactivada), `bifrost_queue`, `bifrost_runner_pool`, `bifrost_admin`; data sources `bifrost_app`, `bifrost_apps`, `bifrost_environment`,
`bifrost_service`, `bifrost_person`.
\* y `bifrost_replication_link` / `bifrost_service_account_key` (que revoca su llave solo con `revoke_on_destroy = true`): la API no tiene la operación inversa, así que destruirlos solo los quita del state (ver [DESIGN.md](DESIGN.md) §2).

## Desarrollo

```sh
make test      # go vet + go test -race: cliente, recursos y ciclos completos con terraform real contra internal/fakeapi
make smoke     # plan -> apply -> plan sin cambios -> import -> destroy de examples/tienda (usa terraform o tofu del PATH)
make build     # bin/terraform-provider-bifrost
make install   # lo deja en ~/.terraform.d/plugins/local/gravity-labs-hq/bifrost para dev_overrides
make docs      # regenera docs/ con tfplugindocs
go run ./cmd/fakeplane   # servidor simulado; imprime su URL; token de prueba: token-directo-ejemplo
```

Para probar un binario local sin registry, usa `dev_overrides` en `~/.terraformrc` (Terraform) o `~/.tofurc` (OpenTofu):

```hcl
provider_installation {
  dev_overrides { "gravity-labs-hq/bifrost" = "/ruta/a/.terraform.d/plugins/local/gravity-labs-hq/bifrost" }
  direct {}
}
```

(En OpenTofu la clave completa es `registry.opentofu.org/gravity-labs-hq/bifrost`.) Con `dev_overrides` no se ejecuta `terraform init` para este provider.

## Contribuir y seguridad

[CONTRIBUTING.md](CONTRIBUTING.md) · [SECURITY.md](SECURITY.md) · [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Licencia [Apache-2.0](LICENSE).
