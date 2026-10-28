package provider

import (
	"regexp"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// El rol manager de cuentas de servicio y vínculos de CI: se acepta, se declara y se valida contra el servidor simulado.
func TestRolManager(t *testing.T) {
	f := newFakeEnv(t)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }

resource "bifrost_service_account" "jefe" {
  app  = bifrost_app.tienda.name
  name = "jefe"
  role = "manager"
}

resource "bifrost_binding" "jefe_ci" {
  app           = bifrost_app.tienda.name
  project_path  = "acme/tienda"
  role          = "manager"
  ref_protected = true
  branches      = ["main"]
  claims        = { environment = "production" }
}
`), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_service_account.jefe", "role", "manager"),
				tfresource.TestCheckResourceAttr("bifrost_binding.jefe_ci", "role", "manager"),
			)},
		},
	})
}

// Un rol inventado sigue rechazándose en el plan; un manager con entornos o un vínculo manager sin sus condiciones lo rechaza el servidor.
func TestRolManagerValidaciones(t *testing.T) {
	app := `resource "bifrost_app" "tienda" { name = "tienda" }
`
	for name, c := range map[string]struct{ cfg, err string }{
		"rol inventado": {`resource "bifrost_service_account" "x" {
  app  = bifrost_app.tienda.name
  name = "xx"
  role = "owner"
}`, `"manager"|deploy`},
		"cuenta manager con entornos": {`resource "bifrost_service_account" "x" {
  app          = bifrost_app.tienda.name
  name         = "xx"
  role         = "manager"
  environments = ["prod"]
}`, "App entera"},
		"vínculo manager sin condiciones": {`resource "bifrost_binding" "x" {
  app          = bifrost_app.tienda.name
  project_path = "acme/tienda"
  role         = "manager"
}`, "ref_protected"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeEnv(t)
			tfresource.UnitTest(t, tfresource.TestCase{
				ProtoV6ProviderFactories: testFactories,
				Steps:                    []tfresource.TestStep{{Config: f.cfg(app + c.cfg), ExpectError: regexp.MustCompile(c.err)}},
			})
		})
	}
}
