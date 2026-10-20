# Contribuir

1. Abre un issue antes de un cambio grande.
2. `make test` (go vet + `go test -race`, con el servidor simulado `internal/fakeapi`; no necesita red ni una instalación de Bifröst) y `make smoke` (necesita `terraform` o `tofu` en el PATH).
3. Si cambias el schema de un recurso, regenera la documentación con `make docs` y commitea el resultado.
4. Commits pequeños y con mensaje claro. Al enviar un PR aceptas que tu aportación se licencia bajo Apache-2.0 (ver [LICENSE](LICENSE)).
5. Seguridad: no abras issues para vulnerabilidades, ver [SECURITY.md](SECURITY.md).

Este repositorio es un **espejo** del subdirectorio `terraform-provider-bifrost` del monorepo de Bifröst; los PR se aplican en el repositorio de origen
y se sincronizan aquí (ver PUBLICAR.md si eres mantenedor).
