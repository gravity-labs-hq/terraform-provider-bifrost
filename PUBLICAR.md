# Publicar el provider (para mantenedores)

Las releases **no se crean a mano ni con GitHub Actions**: las publica el pipeline de CI del repositorio de origen (privado) cuando se crea una etiqueta `vX.Y.Z` protegida.
Ese pipeline sincroniza este repositorio (instantánea del directorio del provider, sin historial privado), crea la etiqueta `vX.Y.Z` aquí y ejecuta GoReleaser
([.goreleaser.yml](.goreleaser.yml)) para crear la release con los zips, `terraform-provider-bifrost_X.Y.Z_SHA256SUMS`, su firma GPG (`.sig`) y `_manifest.json`.
La versión del provider es la de la etiqueta. Por eso **no se acepta ni se fusiona código directamente aquí**: los PR se aplican en el origen (ver [CONTRIBUTING.md](CONTRIBUTING.md)).

## Requisitos únicos (los hace el dueño de la organización)

1. Crear este repositorio en la organización `gravity-labs-hq` con el nombre exacto `terraform-provider-bifrost`, **VACÍO** (sin README, licencia ni .gitignore generados por GitHub, para no chocar con la primera sincronización) y **PÚBLICO**
   (el Registry de Terraform/OpenTofu lo exige). Habilitar *Settings > Code security > Private vulnerability reporting* (lo usa [SECURITY.md](SECURITY.md)).
2. La sincronización se hace **por HTTPS con un token de GitHub** del CI de origen (las deploy keys están deshabilitadas por política de la organización): el token necesita escritura de contenido en este repositorio y `main` no debe estar protegida contra él.
3. Clave GPG de firma (RSA 4096; el Registry no admite ECC). Huella registrada: **`612618EAF9BD703F0B11378EC7E9628075C6B259`**; la clave pública está en [gpg-public-key.asc](gpg-public-key.asc) y la privada solo como variable de CI del origen. Registrarla:
   - **Terraform Registry** ([guía](https://developer.hashicorp.com/terraform/registry/providers/publishing#preparing-and-adding-a-signing-key)): <https://registry.terraform.io/settings/gpg-keys> > *New GPG Key* > namespace `gravity-labs-hq` > pegar el contenido de `gpg-public-key.asc`.
   - **OpenTofu** ([opentofu/registry](https://github.com/opentofu/registry)): la solicitud de alta del provider y su clave pública se envían allí (hoy, por PR; lee su README por si la ruta exacta, `keys/g/gravity-labs-hq/<huella>.asc`, ha cambiado). Comprueba la huella con `gpg --show-keys gpg-public-key.asc`.
4. Tras la primera release, *Publish > Provider* en <https://registry.terraform.io> eligiendo este repositorio, y la solicitud de alta en OpenTofu. Después, cada etiqueta nueva se publica sola (webhook).

Las variables de CI del origen y el procedimiento de release están en `docs/publicacion/PUBLICAR.md` del repositorio de origen.

## Probar el empaquetado en local

`make release-snapshot` (necesita goreleaser v2 y una clave GPG desechable: exporta `GPG_FINGERPRINT` y `GPG_PASSPHRASE`). No publica nada.

## Retirar una versión mala

Ver «Retirar una versión» en el documento del origen: en el Registry de Terraform no se pueden borrar versiones desde la interfaz, se *yankea* publicando una versión corregida y, si es grave, solicitando la retirada al soporte del registry.
