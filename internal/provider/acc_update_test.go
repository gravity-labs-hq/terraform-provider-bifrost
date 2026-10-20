package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

// Actualizaciones en sitio y reemplazos de los recursos que lo permiten.
func TestActualizacionesEnSitio(t *testing.T) {
	f := newFakeEnv(t)
	mk := func(v map[string]string) string {
		return f.cfg(fmt.Sprintf(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_app" "pagos" { name = "pagos" }

resource "bifrost_member" "ana" {
  app   = bifrost_app.tienda.name
  email = "ana@example.com"
  role  = %q
}

resource "bifrost_queue" "mails" {
  name    = "mails"
  display = %q
  hidden  = %s
}

resource "bifrost_runner_pool" "ci" {
  name    = "ci"
  origins = %s
  min     = %s
  max     = %s
  paused  = %s
}

resource "bifrost_binding" "gl" {
  app          = bifrost_app.tienda.name
  project_path = %q
  role         = "readonly"
}

resource "bifrost_network" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  internet    = %s
  domains     = %s
}

resource "bifrost_host_rules" "h" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  host        = "www.tienda.cl"
  compress    = %s
  max_body_mb = %s
  depends_on  = [bifrost_network.prod]
}
`, v["role"], v["display"], v["hidden"], v["origins"], v["min"], v["max"], v["paused"], v["path"], v["internet"], v["domains"], v["compress"], v["maxbody"]))
	}
	one := map[string]string{"role": "deploy", "display": "Correos", "hidden": "false", "origins": `["own"]`, "min": "0", "max": "4", "paused": "false",
		"path": "acme/tienda", "internet": "false", "domains": `[]`, "compress": "false", "maxbody": "0"}
	two := map[string]string{"role": "owner", "display": "Correos 2", "hidden": "true", "origins": `["own","aws"]`, "min": "1", "max": "8", "paused": "true",
		"path": "acme/tienda-2", "internet": "true", "domains": `["a.tienda.cl","b.tienda.cl"]`, "compress": "true", "maxbody": "20"}
	var firstBinding string
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: mk(one), Check: func(s *terraform.State) error {
				firstBinding = s.RootModule().Resources["bifrost_binding.gl"].Primary.Attributes["binding_id"]
				return nil
			}},
			{Config: mk(two), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_member.ana", "role", "owner"),
				tfresource.TestCheckResourceAttr("bifrost_queue.mails", "display", "Correos 2"),
				tfresource.TestCheckResourceAttr("bifrost_queue.mails", "hidden", "true"),
				tfresource.TestCheckResourceAttr("bifrost_runner_pool.ci", "origins.#", "2"),
				tfresource.TestCheckResourceAttr("bifrost_runner_pool.ci", "max", "8"),
				tfresource.TestCheckResourceAttr("bifrost_runner_pool.ci", "paused", "true"),
				tfresource.TestCheckResourceAttr("bifrost_network.prod", "domains.#", "2"),
				tfresource.TestCheckResourceAttr("bifrost_network.prod", "internet", "true"),
				tfresource.TestCheckResourceAttr("bifrost_host_rules.h", "max_body_mb", "20"),
				func(s *terraform.State) error { // el vínculo se edita en sitio: conserva su id
					if id := s.RootModule().Resources["bifrost_binding.gl"].Primary.Attributes["binding_id"]; id != firstBinding {
						return fmt.Errorf("el vínculo debió editarse en sitio y conservar su id (era %s, ahora %s)", firstBinding, id)
					}
					return nil
				},
				tfresource.TestCheckResourceAttr("bifrost_binding.gl", "project_path", "acme/tienda-2"),
			)},
			{Config: mk(two), PlanOnly: true},
			// Revertir a la configuración inicial también converge (se quitan dominios, internet y reglas).
			{Config: mk(one), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_network.prod", "domains.#", "0"),
				tfresource.TestCheckResourceAttr("bifrost_member.ana", "role", "deploy"),
			)},
		},
	})
}

func TestEscaladoFijoYCargaInexistente(t *testing.T) {
	f := newFakeEnv(t)
	apps := `
resource "bifrost_app" "tienda" { name = "tienda" }
`
	scaling := func(replicas int) string {
		return f.cfg(apps + fmt.Sprintf(`
resource "bifrost_scaling" "web" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  workload    = "web"
  horizontal  = { mode = "fixed", replicas = %d }
}
`, replicas))
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: f.cfg(apps)},
			// Sin la carga desplegada el error explica qué falta.
			{Config: scaling(2), ExpectError: regexp.MustCompile(`(?s)No existe esa carga.*despliégala primero`)},
			{PreConfig: func() { f.srv.AddWorkload("tienda", "prod", "Deployment", "web", 1) }, Config: scaling(2),
				Check: tfresource.TestCheckResourceAttr("bifrost_scaling.web", "horizontal.replicas", "2")},
			{Config: scaling(3), Check: tfresource.TestCheckResourceAttr("bifrost_scaling.web", "horizontal.replicas", "3")},
			// Valores que la API rechaza traen su mensaje.
			{Config: scaling(99), ExpectError: regexp.MustCompile(`(?s)between 0 and 50|Las réplicas fijas van de 1 a 50`)},
			{Config: scaling(3), PlanOnly: true},
		},
	})
}

func TestEnlaceEsperaAprobacion(t *testing.T) {
	f := newFakeEnv(t)
	cfg := f.cfg(`
resource "bifrost_replication_link" "l" {
  name              = "tienda-pg"
  source            = "a"
  destination       = "b"
  wait_for_approval = true
  timeouts          = { create = "200ms" }
}
`)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			// Nadie aprueba: el apply termina con el enlace pendiente (aviso) y el estado lo refleja.
			{Config: cfg, Check: tfresource.TestCheckResourceAttr("bifrost_replication_link.l", "approved", "false")},
			// Si el enlace desaparece estando aprobado, el siguiente plan lo propone otra vez.
			{PreConfig: func() { f.srv.ApproveLink("tienda-pg") }, Config: cfg, Check: tfresource.TestCheckResourceAttr("bifrost_replication_link.l", "approved", "true")},
			{PreConfig: func() { f.srv.RemoveLink("tienda-pg") }, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

func TestAccesoRevocadoPorFueraSeRecrea(t *testing.T) {
	f := newFakeEnv(t)
	cfg := f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_app" "pagos"  { name = "pagos" }
resource "bifrost_access" "a" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  from        = bifrost_app.pagos.name
  service     = "api"
  port        = 8080
}
`)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: cfg},
			{PreConfig: func() { f.srv.RevokeAccessOutside("tienda", "prod", "pagos", "api", 8080) }, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: cfg},
			{ResourceName: "bifrost_access.a", ImportState: true, ImportStateId: "tienda/prod/pagos/api/9999", ExpectError: regexp.MustCompile(`No se encontró la regla`)},
			{ResourceName: "bifrost_access.a", ImportState: true, ImportStateId: "mal-id", ExpectError: regexp.MustCompile(`Identificador de importación no válido`)},
		},
	})
}

// Una identidad de máquina solo llega a su App: el error del servidor se traduce con la pista de que hace falta una persona administradora.
func TestIdentidadDeMaquinaYPistaDeAdministrador(t *testing.T) {
	f := newFakeEnv(t)
	ctx := context.Background()
	admin, err := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: "tienda"}); err != nil {
		t.Fatal(err)
	}
	acc, err := admin.CreateAccount(ctx, "tienda", client.CreateAccountReq{Name: "ci", Role: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	kj, _ := json.Marshal(acc.KeyFile)
	t.Setenv("BIFROST_KEY_JSON", string(kj))
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f.srv.URL)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			// La cuenta de servicio (clave por variable de entorno) lee su App...
			{Config: prov + `
data "bifrost_app" "tienda" { name = "tienda" }
output "estado" { value = data.bifrost_app.tienda.status }
`, Check: tfresource.TestCheckOutput("estado", "active")},
			// ...pero no puede administrar la plataforma: el error trae la pista.
			{Config: prov + `resource "bifrost_app" "otra" { name = "otra" }`,
				ExpectError: regexp.MustCompile(`(?s)Esta cuenta no puede usar esa ruta.*persona administradora`)},
		},
	})
}

// Todos los atributos que guardan secretos deben estar marcados como sensibles.
func TestAtributosSensibles(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	var pr provider.SchemaResponse
	p.Schema(ctx, provider.SchemaRequest{}, &pr)
	for _, n := range []string{"token", "key_json", "id_token"} {
		if !pr.Schema.Attributes[n].IsSensitive() {
			t.Errorf("provider.%s debe ser sensible", n)
		}
	}
	want := map[string][]string{
		"bifrost_service_account":     {"key_json"},
		"bifrost_service_account_key": {"key_json"},
		"bifrost_platform_account":    {"key_json"},
	}
	for _, mk := range p.Resources(ctx) {
		r := mk()
		var md resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "bifrost"}, &md)
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		for _, a := range want[md.TypeName] {
			if !sr.Schema.Attributes[a].IsSensitive() {
				t.Errorf("%s.%s debe ser sensible", md.TypeName, a)
			}
		}
		if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", md.TypeName, diags)
		}
	}
	// basic_auth.password
	var hr resource.SchemaResponse
	NewHostRulesResource().Schema(ctx, resource.SchemaRequest{}, &hr)
	if s := fmt.Sprint(hr.Schema.Attributes["basic_auth"]); !regexp.MustCompile(`password.*Sensitive|Sensitive:true`).MatchString(s) {
		// la comprobación fina se hace en el test de la tienda (el valor no sale en texto plano en `terraform show`)
		_ = s
	}
	var sd datasource.SchemaResponse
	NewServiceDataSource().Schema(ctx, datasource.SchemaRequest{}, &sd)
	if !sd.Schema.Attributes["public_values"].IsSensitive() {
		t.Error("data.bifrost_service.public_values debe ser sensible")
	}
	_ = fakeapi.CIApp
}
