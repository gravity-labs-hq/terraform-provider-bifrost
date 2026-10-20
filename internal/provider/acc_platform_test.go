package provider

import (
	"fmt"
	"regexp"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

// Bootstrap: la persona administradora crea la cuenta de plataforma UNA vez; rotar y destruir.
func TestCuentaDePlataformaBootstrap(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	cfg := func(days int, keeper string) string {
		return f.cfg(fmt.Sprintf(`
resource "bifrost_platform_account" "terraform" {
  name            = "terraform"
  scopes          = ["apps:create", "apps:manage", "network:manage", "queues:manage"]
  app_prefix      = "tienda-"
  allowed_domains = ["example.org"]
  expires_in_days = %d
  keepers         = { rotacion = %q }
  ci_binding = {
    project_path = "acme/infra"
    environment  = "production"
  }
}
`, days, keeper))
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: cfg(30, "a"), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_platform_account.terraform", "account_id", "terraform@platform"),
				tfresource.TestCheckResourceAttr("bifrost_platform_account.terraform", "key_id", "k1"),
				tfresource.TestCheckResourceAttrSet("bifrost_platform_account.terraform", "key_json"),
				tfresource.TestCheckResourceAttr("bifrost_platform_account.terraform", "scopes.#", "4"),
				tfresource.TestCheckResourceAttr("bifrost_platform_account.terraform", "effective_scopes.#", "6"), // con las lecturas implícitas
			)},
			{Config: cfg(30, "a"), PlanOnly: true},
			// Renovar/rotar: cambiar la caducidad o los keepers genera otra clave.
			{Config: cfg(60, "a"), Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.terraform", "key_id", "k2")},
			{Config: cfg(60, "b"), Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.terraform", "key_id", "k3")},
			{ResourceName: "bifrost_platform_account.terraform", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"key_json", "key_id", "expires_in_days", "keepers", "ci_binding"}},
			// Validaciones del plan: scopes inexistentes, prefijo obligatorio para colas, caducidad fuera de rango.
			{Config: cfg(60, "b") + `resource "bifrost_platform_account" "x" {
  name            = "otra"
  scopes          = ["admins:manage"]
  expires_in_days = 30
}`, ExpectError: regexp.MustCompile(`(?s)Invalid Attribute Value|must be one of`)},
			{Config: cfg(60, "b") + `resource "bifrost_platform_account" "x" {
  name            = "otra"
  scopes          = ["queues:manage"]
  expires_in_days = 30
}`, ExpectError: regexp.MustCompile(`exigen\s+app_prefix`)},
			{Config: cfg(60, "b") + `resource "bifrost_platform_account" "x" {
  name            = "otra"
  scopes          = ["apps:read"]
  expires_in_days = 4000
}`, ExpectError: regexp.MustCompile(`between 1 and 365`)},
			{Config: cfg(60, "b"), PlanOnly: true},
		},
		CheckDestroy: func(*terraform.State) error {
			f.srv.Lock()
			defer f.srv.Unlock()
			return nil
		},
	})
}

const platformStore = `
resource "bifrost_app" "web" {
  name              = "tienda-web"
  owners            = ["ana@example.org", "luis@example.org"]
  first_environment = "prod"
}

resource "bifrost_app" "pagos" {
  name              = "tienda-pagos"
  owners            = ["ana@example.org"]
  first_environment = "prod"
}

resource "bifrost_environment" "prod" {
  app            = bifrost_app.web.name
  name           = "prod"
  adopt_existing = true
}

resource "bifrost_environment" "dev" {
  app  = bifrost_app.web.name
  name = "dev"
}

resource "bifrost_service" "pg" {
  app               = bifrost_app.web.name
  environment       = bifrost_environment.prod.name
  service           = "postgres"
  confirm_data_loss = true
}

resource "bifrost_service_account" "deploy" {
  app          = bifrost_app.web.name
  name         = "deploy"
  role         = "deploy"
  environments = ["prod"]
  depends_on   = [bifrost_environment.prod]
}

resource "bifrost_binding" "gitlab" {
  app          = bifrost_app.web.name
  project_path  = "acme/tienda"
  role          = "deploy"
  ref_protected = true
  claims        = { environment = "production" }
  environments  = ["prod"]
  depends_on    = [bifrost_environment.prod]
}

resource "bifrost_member" "ana" {
  app   = bifrost_app.web.name
  email = "marta@example.org"
  role  = "readonly"
}

resource "bifrost_quota" "web" {
  app    = bifrost_app.web.name
  cpu    = "4"
  memory = "8Gi"
  pods   = 40
  pvcs   = 10
}

resource "bifrost_network" "prod" {
  app         = bifrost_app.web.name
  environment = bifrost_environment.prod.name
  domains     = ["www.tienda.example.org"]
}

resource "bifrost_host_rules" "www" {
  app              = bifrost_app.web.name
  environment      = bifrost_environment.prod.name
  host             = "www.tienda.example.org"
  security_headers = true
  depends_on       = [bifrost_network.prod]
}

resource "bifrost_access" "pagos" {
  app         = bifrost_app.web.name
  environment = bifrost_environment.prod.name
  from        = bifrost_app.pagos.name
  service     = "api"
  port        = 8080
  depends_on  = [bifrost_environment.prod]
}

resource "bifrost_queue" "correos" {
  name = "tienda-correos"
}

resource "bifrost_runner_pool" "ci" {
  name    = "tienda-ci"
  origins = ["own"]
  app     = bifrost_app.web.name
}

resource "bifrost_replication_link" "pg" {
  name        = "tienda-pg"
  source      = "a"
  destination = "b"
  app         = bifrost_app.web.name
}

data "bifrost_apps" "mias" {
  depends_on = [bifrost_app.web, bifrost_app.pagos]
}
`

// Terraform corre con la cuenta de plataforma (clave por variable de entorno): administra su parte de la plataforma sin ser administrador.
func TestOperarConCuentaDePlataforma(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	key := f.srv.CreatePlatformAccount("terraform", []string{"apps:create", "apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage",
		"queues:manage", "runners:manage", "replication:propose"}, nil, "tienda-", false, "example.org")
	t.Setenv("BIFROST_KEY_JSON", key)
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f.srv.URL)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: func(*terraform.State) error {
			for _, a := range []string{"tienda-web", "tienda-pagos"} {
				if ok, st := f.srv.AppExists(a); !ok || st != "archived" {
					return fmt.Errorf("%s debe quedar archivada, está %v/%q", a, ok, st)
				}
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{Config: prov + platformStore, Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_app.web", "managed_by", "terraform"),
				tfresource.TestCheckResourceAttr("bifrost_app.web", "owners.#", "2"),
				tfresource.TestCheckResourceAttr("bifrost_environment.dev", "status", "active"),
				tfresource.TestCheckResourceAttr("bifrost_service.pg", "secret_name", "postgres-conn"),
				tfresource.TestCheckResourceAttrSet("bifrost_service_account.deploy", "key_json"),
				tfresource.TestCheckResourceAttrSet("bifrost_binding.gitlab", "binding_id"),
				tfresource.TestCheckResourceAttr("bifrost_quota.web", "pods", "40"),
				tfresource.TestCheckResourceAttr("bifrost_network.prod", "domains.#", "1"),
				tfresource.TestCheckResourceAttr("bifrost_access.pagos", "from", "tienda-pagos"),
				tfresource.TestCheckResourceAttr("bifrost_queue.correos", "id", "bullmq/tienda-correos"),
				tfresource.TestCheckResourceAttr("bifrost_runner_pool.ci", "app", "tienda-web"),
				tfresource.TestCheckResourceAttr("bifrost_replication_link.pg", "approved", "false"),
				tfresource.TestCheckResourceAttr("data.bifrost_apps.mias", "names.#", "2"),
			)},
			{Config: prov + platformStore, PlanOnly: true},
			// Lo que la cuenta NO puede, falla con el 403/404 del servidor y la pista.
			{Config: prov + platformStore + `resource "bifrost_admin" "x" { email = "yo@example.org" }`, ExpectError: regexp.MustCompile(`(?s)Esta cuenta de plataforma no puede usar esa\s+ruta.*persona\s+administradora`)},
			{Config: prov + platformStore + `resource "bifrost_app" "fuera" {
  name   = "otra-cosa"
  owners = ["ana@example.org"]
}`, ExpectError: regexp.MustCompile(`dentro de su\s+lista o prefijo`)},
			{Config: prov + platformStore + `resource "bifrost_queue" "ajena" { name = "otra-cola" }`, ExpectError: regexp.MustCompile(`fuera del\s+prefijo`)},
			{Config: prov + platformStore + `resource "bifrost_platform_account" "copia" {
  name            = "copia"
  scopes          = ["apps:read"]
  expires_in_days = 5
}`, ExpectError: regexp.MustCompile(`Esta cuenta de plataforma no puede usar esa\s+ruta`)},
			{Config: prov + platformStore, PlanOnly: true},
		},
	})
}

// El job de CI asume la cuenta de plataforma con su ID token (sin secretos guardados).
func TestOperarConFederacionDePlataforma(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	_ = f.srv.CreatePlatformAccount("terraform", []string{"apps:create", "apps:manage"}, nil, "tienda-", true, "example.org")
	t.Setenv("CI_JOB_JWT_V2", fakeapi.CIIDToken)
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n  id_token_env = \"CI_JOB_JWT_V2\"\n  federated_platform = \"terraform\"\n}\n", f.srv.URL)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: prov + `resource "bifrost_app" "web" {
  name   = "tienda-web"
  owners = ["ana@example.org"]
}`, Check: tfresource.TestCheckResourceAttr("bifrost_app.web", "managed_by", "terraform")},
			// Cuenta sin vínculo de CI, o con otro ID token: el canje se rechaza.
			{Config: fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n  id_token_env = \"CI_JOB_JWT_V2\"\n  federated_platform = \"inexistente\"\n}\n", f.srv.URL) + `data "bifrost_apps" "x" {}`,
				ExpectError: regexp.MustCompile(`plane rechazó la credencial`)},
			{Config: fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n  id_token_env = \"CI_JOB_JWT_V2\"\n  federated_platform = \"terraform\"\n  federated_app = \"x\"\n}\n", f.srv.URL) + `data "bifrost_apps" "x" {}`,
				ExpectError: regexp.MustCompile(`excluyentes`)},
			{Config: prov + `resource "bifrost_app" "web" {
  name   = "tienda-web"
  owners = ["ana@example.org"]
}`, PlanOnly: true},
		},
	})
}
