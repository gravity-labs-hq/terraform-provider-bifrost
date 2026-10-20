package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const storeBase = `
resource "bifrost_app" "tienda" {
  name              = "tienda"
  description       = "La tienda"
  first_environment = "prod"
}

resource "bifrost_app" "pagos" {
  name              = "pagos"
  first_environment = "prod"
}

resource "bifrost_quota" "tienda" {
  app    = bifrost_app.tienda.name
  cpu    = "4"
  memory = "8Gi"
  pods   = 40
  pvcs   = 10
}

resource "bifrost_environment" "prod" {
  app            = bifrost_app.tienda.name
  name           = "prod"
  adopt_existing = true
}

resource "bifrost_environment" "dev" {
  app  = bifrost_app.tienda.name
  name = "dev"
  quota = {
    cpu    = "1"
    memory = "2Gi"
    pods   = 10
    pvcs   = 2
  }
  depends_on = [bifrost_quota.tienda]
}

resource "bifrost_service" "postgres" {
  app               = bifrost_app.tienda.name
  environment       = bifrost_environment.prod.name
  service           = "postgres"
  options           = { version = "16", database = "tienda" }
  confirm_data_loss = true
}

resource "bifrost_service" "redis" {
  app               = bifrost_app.tienda.name
  environment       = bifrost_environment.prod.name
  service           = "redis"
  confirm_data_loss = true
}

resource "bifrost_service_account" "deploy" {
  app          = bifrost_app.tienda.name
  name         = "deploy"
  role         = "deploy"
  environments = [bifrost_environment.prod.name]
}

resource "bifrost_service_account_key" "deploy" {
  app     = bifrost_app.tienda.name
  account = bifrost_service_account.deploy.name
  keepers = { rotacion = "2026-10" }
}

resource "bifrost_binding" "gitlab" {
  app          = bifrost_app.tienda.name
  project_path = "acme/tienda"
  role         = "deploy"
  ref_protected = true
  claims       = { environment = "production" }
  environments = [bifrost_environment.prod.name]
}

resource "bifrost_member" "ana" {
  app   = bifrost_app.tienda.name
  email = "ana@example.com"
  role  = "deploy"
}

resource "bifrost_network" "prod" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  domains     = ["www.tienda.cl"]
  internet    = true
  internet_ports = [443]
}

resource "bifrost_network" "dev" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.dev.name
  egress = [{ cidr = "10.0.0.0/24", port = 5432, reason = "base externa" }]
}

resource "bifrost_host_rules" "www" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  host        = "www.tienda.cl"
  ip_deny     = ["203.0.113.0/24"]
  security_headers = true
  rate_limit = { average = 100, period_seconds = 1, burst = 50 }
  basic_auth = [{ name = "qa", password = "contraseña-de-ejemplo" }]
  cors = { origins = ["https://tienda.cl"], methods = ["GET"] }
  depends_on = [bifrost_network.prod]
}

resource "bifrost_access" "pagos_a_tienda" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  from        = bifrost_app.pagos.name
  service     = "api"
  port        = 8080
}

resource "bifrost_queue" "mails" {
  name    = "mails"
  display = "Correos"
}

resource "bifrost_queue" "eventos" {
  kind = "jetstream"
  name = "eventos"
  jetstream = {
    subjects  = ["eventos.>"]
    retention = "workqueue"
    replicas  = 1
    storage   = "file"
    max_age_seconds = 86400
  }
  confirm_data_loss = true
}

resource "bifrost_runner_pool" "ci" {
  name    = "tienda-ci"
  labels  = ["docker"]
  origins = ["own"]
  max     = 6
  app     = bifrost_app.tienda.name
}

resource "bifrost_admin" "ops" {
  email = "ops@example.com"
}

resource "bifrost_replication_link" "pg" {
  name        = "tienda-pg"
  source      = "pg-prod"
  destination = "pg-replica"
  app         = bifrost_app.tienda.name
}

data "bifrost_app" "tienda" {
  name       = bifrost_app.tienda.name
  depends_on = [bifrost_environment.dev]
}

data "bifrost_apps" "todas" {
  depends_on = [bifrost_app.tienda, bifrost_app.pagos]
}

data "bifrost_environment" "prod" {
  app        = bifrost_app.tienda.name
  name       = "prod"
  depends_on = [bifrost_network.prod, bifrost_service.postgres]
}

data "bifrost_service" "postgres" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  service     = bifrost_service.postgres.service
}

data "bifrost_person" "ana" {
  email      = "ana@example.com"
  depends_on = [bifrost_member.ana]
}
`

func TestTiendaCompleta(t *testing.T) {
	f := newFakeEnv(t)
	prepare := func() {} // las cargas se simulan después de crear el entorno
	_ = prepare
	scaling := `
resource "bifrost_scaling" "web" {
  app         = bifrost_app.tienda.name
  environment = bifrost_environment.prod.name
  workload    = "web"
  horizontal = {
    mode          = "auto"
    min           = 2
    max           = 6
    cpu_target    = 70
  }
  vertical = { mode = "recommend" }
}
`
	cfg1 := f.cfg(storeBase)
	cfg2 := f.cfg(storeBase + scaling)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: cfg1, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("bifrost_app.tienda", "status", "active"),
				resource.TestCheckResourceAttr("bifrost_quota.tienda", "pods", "40"),
				resource.TestCheckResourceAttr("bifrost_environment.prod", "id", "tienda/prod"),
				resource.TestCheckResourceAttr("bifrost_environment.prod", "status", "active"),
				resource.TestCheckResourceAttr("bifrost_environment.dev", "quota.pods", "10"),
				resource.TestCheckResourceAttr("bifrost_environment.dev", "namespace", "app-tienda-dev"),
				resource.TestCheckResourceAttr("bifrost_service.postgres", "secret_name", "postgres-conn"),
				resource.TestCheckResourceAttr("bifrost_service.postgres", "id", "tienda/prod/postgres"),
				resource.TestCheckResourceAttr("bifrost_service_account.deploy", "account_id", "deploy@app-tienda"),
				resource.TestCheckResourceAttrSet("bifrost_service_account.deploy", "key_json"),
				resource.TestCheckResourceAttr("bifrost_service_account_key.deploy", "key_id", "k2"),
				resource.TestCheckResourceAttrSet("bifrost_service_account_key.deploy", "key_json"),
				resource.TestCheckResourceAttrSet("bifrost_binding.gitlab", "binding_id"),
				resource.TestCheckResourceAttr("bifrost_binding.gitlab", "claims.environment", "production"),
				resource.TestCheckResourceAttr("bifrost_member.ana", "role", "deploy"),
				resource.TestCheckResourceAttr("bifrost_network.prod", "domains.#", "1"),
				resource.TestCheckResourceAttr("bifrost_network.prod", "internet_ports.#", "1"),
				resource.TestCheckResourceAttr("bifrost_network.dev", "egress.0.cidr", "10.0.0.0/24"),
				resource.TestCheckResourceAttr("bifrost_host_rules.www", "rate_limit.average", "100"),
				resource.TestCheckResourceAttr("bifrost_host_rules.www", "basic_auth.0.password", "contraseña-de-ejemplo"),
				resource.TestCheckResourceAttr("bifrost_access.pagos_a_tienda", "id", "tienda/prod/pagos/api/8080"),
				resource.TestCheckResourceAttr("bifrost_queue.mails", "id", "bullmq/mails"),
				resource.TestCheckResourceAttr("bifrost_queue.eventos", "jetstream.retention", "workqueue"),
				resource.TestCheckResourceAttr("bifrost_runner_pool.ci", "max", "6"),
				resource.TestCheckResourceAttr("bifrost_admin.ops", "source", "bifrost"),
				resource.TestCheckResourceAttr("bifrost_replication_link.pg", "approved", "false"),
				resource.TestCheckResourceAttr("data.bifrost_app.tienda", "environments.#", "2"),
				resource.TestCheckResourceAttr("data.bifrost_apps.todas", "names.#", "2"),
				resource.TestCheckResourceAttr("data.bifrost_environment.prod", "services.#", "2"),
				resource.TestCheckResourceAttr("data.bifrost_service.postgres", "public_values.host", "postgres.prod.svc"),
				resource.TestCheckResourceAttr("data.bifrost_service.postgres", "secret_keys.0", "password"),
				resource.TestCheckResourceAttr("data.bifrost_person.ana", "apps.tienda", "deploy"),
			)},
			// Sin cambios: el plan queda vacío (lectura estable de todos los recursos).
			{Config: cfg1, PlanOnly: true},
			// Escalado: la carga la crea el despliegue; aquí se simula.
			{PreConfig: func() { f.srv.AddWorkload("tienda", "prod", "Deployment", "web", 2) }, Config: cfg2, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("bifrost_scaling.web", "horizontal.max", "6"),
				resource.TestCheckResourceAttr("bifrost_scaling.web", "vertical.mode", "recommend"),
			)},
			{Config: cfg2, PlanOnly: true},
			// Aprobación del enlace en Ratatoskr: el siguiente refresh lo refleja.
			{PreConfig: func() { f.srv.ApproveLink("tienda-pg") }, Config: cfg2, Check: resource.TestCheckResourceAttr("bifrost_replication_link.pg", "approved", "true")},
			// Drift de red hecho por fuera.
			{PreConfig: func() { f.srv.SetNetworkOutside("tienda", "prod", []string{"otro.tienda.cl"}) }, Config: cfg2, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: cfg2, Check: resource.TestCheckResourceAttr("bifrost_network.prod", "domains.#", "1")},
			// Importaciones (id compuesto app/env/...).
			{ResourceName: "bifrost_app.tienda", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_environment.dev", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_environment.prod", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"adopt_existing"}},
			{ResourceName: "bifrost_quota.tienda", ImportState: true, ImportStateId: "tienda", ImportStateVerify: true},
			{ResourceName: "bifrost_service.redis", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"confirm_data_loss"}},
			{ResourceName: "bifrost_service_account.deploy", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"key_json", "key_id", "expires_in_days"}},
			{ResourceName: "bifrost_member.ana", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_binding.gitlab", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_network.prod", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_network.dev", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_host_rules.www", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"basic_auth"}},
			{ResourceName: "bifrost_access.pagos_a_tienda", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_scaling.web", ImportState: true, ImportStateId: "tienda/prod/Deployment/web", ImportStateVerify: true},
			{ResourceName: "bifrost_queue.mails", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"confirm_data_loss"}},
			{ResourceName: "bifrost_queue.eventos", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"confirm_data_loss"}},
			{ResourceName: "bifrost_runner_pool.ci", ImportState: true, ImportStateVerify: true},
			{ResourceName: "bifrost_admin.ops", ImportState: true, ImportStateVerify: true},
		},
	})
}

// Cada destroy debe dejar la plataforma como estaba: las cuentas revocadas, los vínculos y miembros fuera, colas y pools quitados.
func TestTiendaDestruccion(t *testing.T) {
	f := newFakeEnv(t)
	check := func(*terraform.State) error {
		if ok, st := f.srv.AppExists("tienda"); !ok || st != "archived" {
			return fmt.Errorf("tienda debe quedar archivada, está %v/%q", ok, st)
		}
		if ex, rev := f.srv.AccountRevoked("tienda", "deploy"); !ex || !rev {
			return fmt.Errorf("la cuenta debe quedar revocada")
		}
		if n := f.srv.CountState(); n != "" {
			return fmt.Errorf("quedaron recursos: %s", n)
		}
		return nil
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy:             check,
		Steps:                    []resource.TestStep{{Config: f.cfg(storeBase)}},
	})
}

func TestServiceProteccionContraPerdidaDeDatos(t *testing.T) {
	f := newFakeEnv(t)
	mk := func(confirm bool) string {
		return f.cfg(fmt.Sprintf(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_service" "pg" {
  app               = bifrost_app.tienda.name
  environment       = "prod"
  service           = "postgres"
  options           = { version = "15" }
  confirm_data_loss = %t
  depends_on        = [bifrost_app.tienda]
}
`, confirm))
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: mk(false)},
			// Cambiar las opciones exige reemplazo (borra datos): sin confirmación falla y no toca nada.
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_service" "pg" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  service     = "postgres"
  options     = { version = "16" }
  depends_on  = [bifrost_app.tienda]
}
`), ExpectError: regexp.MustCompile(`(?s)Protección contra pérdida de datos.*confirm_data_loss`)},
			{Config: mk(false), Destroy: true, ExpectError: regexp.MustCompile(`Protección contra pérdida de datos`)},
			{Config: mk(true)},
			{Config: mk(true), Destroy: true},
		},
	})
}

func TestEntornoCuotaInmutableYAdopcion(t *testing.T) {
	f := newFakeEnv(t)
	cfg := func(extra string) string {
		return f.cfg(fmt.Sprintf(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_environment" "dev" {
  app  = bifrost_app.tienda.name
  name = "dev"
  %s
}
`, extra))
	}
	q := func(pods int) string {
		return fmt.Sprintf(`quota = { cpu = "1", memory = "2Gi", pods = %d, pvcs = 1 }`, pods)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: cfg(q(10)), Check: resource.TestCheckResourceAttr("bifrost_environment.dev", "quota.pods", "10")},
			{Config: cfg(q(20)), ExpectError: regexp.MustCompile(`(?s)La cuota de un entorno no se puede cambiar.*bifrost_quota`)},
			// Omitir `quota` conserva la actual sin diferencias.
			{Config: cfg(""), PlanOnly: true},
			// Quitar un entorno por fuera -> se recrea.
			{PreConfig: func() { f.srv.DropEnv("tienda", "dev") }, Config: cfg(q(10)), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: cfg(q(10)), Check: resource.TestCheckResourceAttr("bifrost_environment.dev", "status", "active")},
			// Un entorno existente sin adopt_existing: el 409 explica qué hacer.
			{Config: cfg(q(10)) + `
resource "bifrost_environment" "dup" {
  app  = bifrost_app.tienda.name
  name = "dev"
}
`, ExpectError: regexp.MustCompile(`Ya existe el entorno`)},
		},
	})
}

func TestCuentaDeServicioRevocadaYClaveExterna(t *testing.T) {
	f := newFakeEnv(t)
	cfg := f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_service_account" "ci" {
  app  = bifrost_app.tienda.name
  name = "ci"
  role = "readonly"
}
resource "bifrost_service_account_key" "ci" {
  app     = bifrost_app.tienda.name
  account = bifrost_service_account.ci.name
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr("bifrost_service_account_key.ci", "key_id", "k2")},
			// La clave se revoca por fuera -> el siguiente plan la rota.
			{PreConfig: func() { f.srv.RevokeKeyOutside("tienda", "ci", "k2") }, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: cfg, Check: resource.TestCheckResourceAttr("bifrost_service_account_key.ci", "key_id", "k3")},
			// Cambiar el rol reemplaza la cuenta; como la revocada sigue ocupando el nombre, el mensaje lo explica.
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_service_account" "ci" {
  app  = bifrost_app.tienda.name
  name = "ci"
  role = "deploy"
}
`), ExpectError: regexp.MustCompile(`(?s)Ya existe una cuenta.*revocada sigue ocupando su nombre`)},
		},
	})
}

func TestErroresDeLaAPIConMensajesUtiles(t *testing.T) {
	f := newFakeEnv(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []resource.TestStep{
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_binding" "malo" {
  app          = bifrost_app.tienda.name
  project_path = "acme/tienda"
  issuer       = "https://otro-gitlab.example.org"
  role         = "deploy"
}`), ExpectError: regexp.MustCompile(`Ese emisor de CI no está permitido`)},
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_member" "m" {
  app   = bifrost_app.tienda.name
  email = "admin@example.com"
  role  = "deploy"
}`), ExpectError: regexp.MustCompile(`(?s)ya es miembro.*terraform import`)},
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_network" "n" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  domains     = ["Mal Dominio"]
}`), ExpectError: regexp.MustCompile(`Dominio no válido`)},
			{Config: f.cfg(`
resource "bifrost_app" "tienda" { name = "tienda" }
resource "bifrost_network" "n" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  internet    = true
  egress      = [{ cidr = "10.0.0.0/8", port = 80 }]
}`), ExpectError: regexp.MustCompile(`excluyentes`)},
			{Config: f.cfg(`
resource "bifrost_scaling" "x" {
  app         = "tienda"
  environment = "prod"
  workload    = "web"
  horizontal  = { mode = "fixed", replicas = 2 }
}
resource "bifrost_app" "tienda" { name = "tienda" }`), ExpectError: regexp.MustCompile(`No existe esa carga|La carga debe existir|No existe esa App`)},
		},
	})
}
