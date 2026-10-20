package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func appStatusIs(f *fakeEnv, name, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		ok, st := f.srv.AppExists(name)
		if want == "" {
			if ok {
				return fmt.Errorf("la App %s debería no existir", name)
			}
			return nil
		}
		if !ok || st != want {
			return fmt.Errorf("App %s: existe=%v estado=%q (quería %q)", name, ok, st, want)
		}
		return nil
	}
}

func TestAppCicloCompleto(t *testing.T) {
	f := newFakeEnv(t)
	base := `
resource "bifrost_app" "tienda" {
  name              = "tienda"
  description       = "La tienda"
  first_environment = "prod"
}
`
	updated := `
resource "bifrost_app" "tienda" {
  name              = "tienda"
  display_name      = "Mi Tienda"
  description       = "La tienda online"
  first_environment = "prod"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             appStatusIs(f, "tienda", "archived"),
		Steps: []resource.TestStep{
			{Config: f.cfg(base), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("bifrost_app.tienda", "id", "tienda"),
				resource.TestCheckResourceAttr("bifrost_app.tienda", "display_name", "tienda"),
				resource.TestCheckResourceAttr("bifrost_app.tienda", "status", "active"),
				resource.TestCheckResourceAttr("bifrost_app.tienda", "owner", "admin@example.com"),
				resource.TestCheckResourceAttr("bifrost_app.tienda", "on_destroy", "archive"),
			)},
			{Config: f.cfg(updated), Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("bifrost_app.tienda", "display_name", "Mi Tienda"),
				resource.TestCheckResourceAttr("bifrost_app.tienda", "description", "La tienda online"),
			)},
			{ResourceName: "bifrost_app.tienda", ImportState: true, ImportStateVerify: true},
			// Drift: alguien cambia la descripción por fuera -> el plan lo ve y lo corrige.
			{PreConfig: func() { f.srv.SetDescription("tienda", "cambiada a mano") }, Config: f.cfg(updated), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: f.cfg(updated), Check: resource.TestCheckResourceAttr("bifrost_app.tienda", "description", "La tienda online")},
			// Borrado externo: el plan propone crearla otra vez.
			{PreConfig: func() { f.srv.Drop("tienda") }, Config: f.cfg(updated), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: f.cfg(updated), Check: appStatusIs(f, "tienda", "active")},
		},
	})
}

func TestAppValidadoresDeNombre(t *testing.T) {
	f := newFakeEnv(t)
	for name, tc := range map[string]struct{ cfg, err string }{
		"mayúsculas":  {`resource "bifrost_app" "x" { name = "Mi-Tienda" }`, "ya normalizado"},
		"un carácter": {`resource "bifrost_app" "x" { name = "a" }`, "length"},
		"reservado":   {`resource "bifrost_app" "x" { name = "kube" }`, "must not be one of|NoneOf|value must be none of"},
		"guion doble": {`resource "bifrost_app" "x" { name = "a--b" }`, "ya normalizado"},
		"largo":       {`resource "bifrost_app" "x" { name = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }`, "length"},
		"entorno":     {"resource \"bifrost_app\" \"x\" {\n name = \"tienda\"\n first_environment = \"Producción\"\n}", "ya normalizado"},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []resource.TestStep{
				{Config: f.cfg(tc.cfg), PlanOnly: true, ExpectError: regexp.MustCompile(tc.err)}}})
		})
	}
}

func TestAppPurgeYRetencion(t *testing.T) {
	f := newFakeEnv(t)
	cfg := `
resource "bifrost_app" "tienda" {
  name       = "tienda"
  on_destroy = "purge"
}
`
	f.srv.Retention = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             appStatusIs(f, "tienda", ""),
		Steps: []resource.TestStep{
			{Config: f.cfg(cfg)},
			// Con la retención en curso el destroy falla con la explicación y conserva el recurso en el state.
			{Config: f.cfg(cfg), Destroy: true, ExpectError: regexp.MustCompile(`(?s)no se pudo eliminar de forma definitiva.*retención`)},
			{PreConfig: func() { f.srv.Retention = false }, Config: f.cfg(cfg), Destroy: true},
		},
	})
}

func TestAppRestaurarArchivada(t *testing.T) {
	f := newFakeEnv(t)
	cfg := func(restore bool) string {
		return f.cfg(fmt.Sprintf(`
resource "bifrost_app" "tienda" {
  name             = "tienda"
  restore_archived = %t
}
`, restore))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: cfg(false)},
			{Config: cfg(false), Destroy: true},
			// Archivada: sin restore_archived falla con pista; con él se restaura.
			{Config: cfg(false), ExpectError: regexp.MustCompile(`(?s)Ya existe.*restore_archived`)},
			{Config: cfg(true), Check: appStatusIs(f, "tienda", "active")},
		},
	})
}

func TestAppFalloDeAprovisionamiento(t *testing.T) {
	f := newFakeEnv(t)
	f.srv.FailProvision["tienda/prod"] = true
	cfg := `
resource "bifrost_app" "tienda" {
  name             = "tienda"
  restore_archived = true
  timeouts = { create = "300ms" }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: f.cfg(cfg), ExpectError: regexp.MustCompile(`(?s)no quedó sana`)},
			// Se arregla el cluster: el recurso dañado se reemplaza (archiva y restaura) y queda activo.
			{PreConfig: func() { delete(f.srv.FailProvision, "tienda/prod") }, Config: f.cfg(cfg), Check: appStatusIs(f, "tienda", "active")},
		},
	})
}

func TestAppErrorSeReconciliaEnSitio(t *testing.T) {
	f := newFakeEnv(t)
	cfg := `resource "bifrost_app" "tienda" { name = "tienda" }`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: f.cfg(cfg)},
			{PreConfig: func() {
				f.srv.Mutate(func() { f.srv.SetEnvStatus("tienda", "prod", "error") })
			}, Config: f.cfg(cfg), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: f.cfg(cfg), Check: resource.TestCheckResourceAttr("bifrost_app.tienda", "status", "active")},
		},
	})
}
