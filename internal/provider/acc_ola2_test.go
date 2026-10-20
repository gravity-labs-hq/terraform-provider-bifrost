package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

// adminClient es un cliente de administrador contra el servidor simulado, para preparar o comprobar estado fuera de Terraform.
func adminClient(t *testing.T, f *fakeEnv) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const tiendaProd = `
resource "bifrost_app" "tienda" {
  name = "tienda"
}

resource "bifrost_network" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  domains     = ["www.tienda.cl"]
}
`

func hostRulesCfg(f *fakeEnv, body string) string {
	return f.cfg(tiendaProd + `
resource "bifrost_host_rules" "www" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  host        = "www.tienda.cl"
  depends_on  = [bifrost_network.prod]
` + body + `
}
`)
}

// Las reglas de una dirección se envían COMPLETAS: aplicar un cambio de otro campo no apaga en silencio lo nuevo de la ola 1 (HTTPS, espera, mantenimiento, notas).
func TestReglasDeDireccionCompletas(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	rules := func() client.HostRules {
		v, err := c.GetHostRules(context.Background(), "tienda", "prod", "www.tienda.cl")
		if err != nil {
			t.Fatal(err)
		}
		return v.Rules
	}
	full := func(compress string) string {
		return fmt.Sprintf(`
  compress       = %s
  https_redirect = true
  timeout_seconds = 120
  ip_deny        = ["203.0.113.0/24", "198.51.100.7/32"]
  ip_allow       = ["10.0.0.0/8"]
  ip_notes       = { "203.0.113.0/24" = "scanner conocido", "10.0.0.0/8" = "oficina" }
  maintenance = {
    html                = "<h1>Volvemos pronto</h1>"
    retry_after_seconds = 600
    allow_ips           = ["192.0.2.10/32"]
  }`, compress)
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: hostRulesCfg(f, full("false")), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_host_rules.www", "https_redirect", "true"),
				tfresource.TestCheckResourceAttr("bifrost_host_rules.www", "timeout_seconds", "120"),
				tfresource.TestCheckResourceAttr("bifrost_host_rules.www", "maintenance.retry_after_seconds", "600"),
				tfresource.TestCheckResourceAttr("bifrost_host_rules.www", "ip_notes.%", "2"),
				func(*terraform.State) error {
					r := rules()
					if !r.HTTPSRedirect || r.TimeoutSeconds != 120 || r.Maintenance == nil || r.Maintenance.RetryAfterSeconds != 600 || r.IPNotes["203.0.113.0/24"].Note != "scanner conocido" {
						return fmt.Errorf("el servidor no recibió lo nuevo: %+v", r)
					}
					return nil
				},
			)},
			{Config: hostRulesCfg(f, full("false")), PlanOnly: true},
			// Cambiar SOLO `compress`: lo demás sigue como estaba en el servidor.
			{Config: hostRulesCfg(f, full("true")), Check: func(*terraform.State) error {
				r := rules()
				if !r.Compress || !r.HTTPSRedirect || r.TimeoutSeconds != 120 || r.Maintenance == nil || r.Maintenance.HTML != "<h1>Volvemos pronto</h1>" || len(r.IPNotes) != 2 || len(r.IPDeny) != 2 {
					return fmt.Errorf("aplicar un cambio apagó reglas de la ola 1: %+v", r)
				}
				return nil
			}},
			// Quitar el bloque de mantenimiento y las notas lo apaga; lo demás se conserva.
			{Config: hostRulesCfg(f, `
  compress        = true
  https_redirect  = true
  timeout_seconds = 120
  ip_deny         = ["203.0.113.0/24"]`), Check: func(*terraform.State) error {
				r := rules()
				if r.Maintenance != nil || len(r.IPNotes) != 0 || len(r.IPDeny) != 1 || !r.HTTPSRedirect {
					return fmt.Errorf("estado inesperado: %+v", r)
				}
				return nil
			}},
			{ResourceName: "bifrost_host_rules.www", ImportState: true, ImportStateVerify: true},
		},
	})
}

// Un cambio hecho fuera de Terraform en lo nuevo se detecta (drift) y un apply lo corrige.
func TestReglasDeDireccionDriftDeLoNuevo(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	cfg := hostRulesCfg(f, `
  https_redirect  = true
  timeout_seconds = 60`)
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: cfg},
			{PreConfig: func() {
				v, _ := c.GetHostRules(context.Background(), "tienda", "prod", "www.tienda.cl")
				r := v.Rules
				r.HTTPSRedirect, r.TimeoutSeconds = false, 5
				if _, err := c.SetHostRules(context.Background(), "tienda", "prod", "www.tienda.cl", r); err != nil {
					t.Fatal(err)
				}
			}, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: cfg, Check: tfresource.TestCheckResourceAttr("bifrost_host_rules.www", "timeout_seconds", "60")},
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestReglasDeDireccionValidaciones(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	cases := map[string]struct{ body, err string }{
		"IP sin máscara no es canónica":   {`ip_deny = ["203.0.113.7"]`, `forma canónica|IP o rango no válido`},
		"rango sin enmascarar":            {`ip_deny = ["203.0.113.5/24"]`, `forma canónica`},
		"todo internet":                   {`ip_allow = ["0.0.0.0/0"]`, `todo internet`},
		"nota de una IP que no está":      {"ip_deny = [\"203.0.113.0/24\"]\n  ip_notes = { \"198.51.100.0/24\" = \"x\" }", `descartaría la nota`},
		"nota vacía":                      {"ip_deny = [\"203.0.113.0/24\"]\n  ip_notes = { \"203.0.113.0/24\" = \"\" }", `string length`},
		"espera fuera de rango":           {`timeout_seconds = 99999`, `between 0 and 3600`},
		"reintento fuera de rango":        {"maintenance = {\n    retry_after_seconds = 999999\n  }", `between 0 and 86400`},
		"IP de mantenimiento sin máscara": {"maintenance = {\n    allow_ips = [\"192.0.2.10\"]\n  }", `IP o rango no válido|forma canónica`},
		"HTML demasiado grande":           {"maintenance = {\n    html = \"" + strings.Repeat("a", 4097) + "\"\n  }", `demasiado largo`},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories,
				Steps: []tfresource.TestStep{{Config: hostRulesCfg(f, tc.body), PlanOnly: true, ExpectError: regexp.MustCompile(tc.err)}}})
		})
	}
}

// Importar unas reglas completas (con lo nuevo) y planear da 0 cambios.
func TestReglasDeDireccionImportar(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	ctx := context.Background()
	if _, err := c.CreateApp(ctx, client.CreateAppReq{Name: "tienda", Owners: []string{"ana@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetNetwork(ctx, "tienda", "prod", client.NetworkReq{Domains: []string{"www.tienda.cl"}, InternetPorts: []int64{}, Egress: []client.Egress{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetHostRules(ctx, "tienda", "prod", "www.tienda.cl", client.HostRules{IPAllow: []string{}, IPDeny: []string{"203.0.113.7"}, // el servidor lo normaliza a /32
		IPNotes: map[string]client.IPNote{"203.0.113.7": {Note: "scanner"}}, BasicAuth: []client.BasicUser{}, HTTPSRedirect: true, TimeoutSeconds: 30,
		Maintenance: &client.Maintenance{HTML: "<p>x</p>", RetryAfterSeconds: 60, AllowIPs: []string{"192.0.2.1"}}}); err != nil {
		t.Fatal(err)
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{{
			Config: f.cfg(`
import {
  to = bifrost_host_rules.www
  id = "tienda/prod/www.tienda.cl"
}

resource "bifrost_host_rules" "www" {
  app             = "tienda"
  environment     = "prod"
  host            = "www.tienda.cl"
  https_redirect  = true
  timeout_seconds = 30
  ip_deny         = ["203.0.113.7/32"]
  ip_notes        = { "203.0.113.7/32" = "scanner" }
  maintenance = {
    html                = "<p>x</p>"
    retry_after_seconds = 60
    allow_ips           = ["192.0.2.1/32"]
  }
}
`),
			ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("bifrost_host_rules.www", plancheck.ResourceActionNoop)}},
		}},
	})
}

// ---- vínculos con ramas ----

func bindingCfg(f *fakeEnv, body string) string {
	return f.cfg(`
resource "bifrost_app" "tienda" {
  name = "tienda"
}

resource "bifrost_binding" "gl" {
  app           = bifrost_app.tienda.name
  project_path  = "acme/tienda"
  role          = "deploy"
  ref_protected = true
  environments  = ["prod"]
` + body + `
}
`)
}

func TestVinculoConRamasSeEditaEnSitio(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	var first string
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: bindingCfg(f, `branches = ["main", "release/*"]`), Check: func(s *terraform.State) error {
				first = s.RootModule().Resources["bifrost_binding.gl"].Primary.Attributes["binding_id"]
				return tfresource.TestCheckResourceAttr("bifrost_binding.gl", "branches.#", "2")(s)
			}},
			{Config: bindingCfg(f, `branches = ["main", "release/*"]`), PlanOnly: true},
			// Cambiar las ramas, el rol y el proyecto se hace en sitio: conserva el id (y el último canje en el servidor real).
			{Config: strings.Replace(bindingCfg(f, `branches = ["main"]`), `role          = "deploy"`, `role          = "readonly"`, 1),
				ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("bifrost_binding.gl", plancheck.ResourceActionUpdate)}},
				Check: func(s *terraform.State) error {
					if id := s.RootModule().Resources["bifrost_binding.gl"].Primary.Attributes["binding_id"]; id != first {
						return fmt.Errorf("el vínculo debió conservar su id: era %s y es %s", first, id)
					}
					return tfresource.ComposeTestCheckFunc(
						tfresource.TestCheckResourceAttr("bifrost_binding.gl", "branches.#", "1"),
						tfresource.TestCheckResourceAttr("bifrost_binding.gl", "role", "readonly"),
					)(s)
				}},
			// Sin ramas = cualquier ref.
			{Config: bindingCfg(f, ``), Check: tfresource.TestCheckResourceAttr("bifrost_binding.gl", "branches.#", "0")},
			{ResourceName: "bifrost_binding.gl", ImportState: true, ImportStateVerify: true},
			// Validaciones.
			{Config: bindingCfg(f, `branches = ["*"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`Rama no válida`)},
			{Config: bindingCfg(f, `branches = ["main..x"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`Rama no válida`)},
			{Config: bindingCfg(f, `branches = ["a b"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`Rama no válida`)},
			{Config: bindingCfg(f, `branches = ["-malo"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`Rama no válida`)},
			{Config: bindingCfg(f, ``), PlanOnly: true}, // el destroy final usa la última configuración: que sea válida
		},
	})
}

// ---- llaves: añadir sin rotar y revocar al destruir ----

func keyCfg(f *fakeEnv, keys string) string {
	return f.cfg(`
resource "bifrost_app" "tienda" {
  name = "tienda"
}

resource "bifrost_service_account" "ci" {
  app          = bifrost_app.tienda.name
  name         = "ci"
  role         = "deploy"
  environments = ["prod"]
}
` + keys)
}

func TestLlavesAnadirSinRotarYRevocarAlDestruir(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	keyState := func() map[string]bool {
		a, err := c.GetApp(context.Background(), "tienda")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, acc := range a.Accounts {
			if acc.Name == "ci" {
				for _, k := range acc.Keys {
					out[k.ID] = k.Revoked
				}
			}
		}
		return out
	}
	extra := `
resource "bifrost_service_account_key" "extra" {
  app               = bifrost_app.tienda.name
  account           = bifrost_service_account.ci.name
  keep_previous     = true
  revoke_on_destroy = true
}
`
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: keyCfg(f, extra), Check: func(*terraform.State) error {
				ks := keyState()
				if len(ks) != 2 || ks["k1"] || ks["k2"] {
					return fmt.Errorf("debía haber dos llaves vigentes: %v", ks)
				}
				return nil
			}},
			{Config: keyCfg(f, extra), PlanOnly: true},
			// Quitar el recurso revoca SOLO esa llave (k2); la de la cuenta (k1) sigue.
			{Config: keyCfg(f, ""), Check: func(*terraform.State) error {
				ks := keyState()
				if !ks["k2"] || ks["k1"] {
					return fmt.Errorf("solo k2 debía quedar revocada: %v", ks)
				}
				return nil
			}},
		},
	})
}

// ---- grupo de la App ----

func TestAppGrupo(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	cfg := func(g string) string {
		return f.cfg(fmt.Sprintf(`
resource "bifrost_app" "tienda" {
  name  = "tienda"
  group = %q
}

data "bifrost_app" "t" {
  name       = bifrost_app.tienda.name
  depends_on = [bifrost_app.tienda]
}
`, g))
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: cfg("pagos"), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_app.tienda", "group", "pagos"),
				tfresource.TestCheckResourceAttr("data.bifrost_app.t", "group", "pagos"))},
			{Config: cfg("pagos"), PlanOnly: true},
			{Config: cfg("web"), Check: tfresource.TestCheckResourceAttr("bifrost_app.tienda", "group", "web")},
			{Config: cfg(""), Check: tfresource.TestCheckResourceAttr("bifrost_app.tienda", "group", "")},
			{Config: cfg("Pagos Raros"), PlanOnly: true, ExpectError: regexp.MustCompile(`minúsculas, números y guiones`)},
			{Config: cfg(""), PlanOnly: true},
			{ResourceName: "bifrost_app.tienda", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"first_environment", "restore_archived", "on_destroy", "timeouts", "owner", "owners", "first_environment_quota"}},
		},
	})
}

// ---- programación de carga ----

func futureDate(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02") }

func scheduleCfg(f *fakeEnv, body string) string {
	return f.cfg(`
resource "bifrost_app" "tienda" {
  name = "tienda"
}

resource "bifrost_schedule" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
` + body + `
}
`)
}

const scheduleBody = `
  timezone = "America/Santiago"
  base     = "noche"
  profiles = {
    normal = { name = "Normal", color = "#2f6fdb", icon = "sun", workloads = { web = { min = 2, max = 3 } } }
    pico   = { name = "Pico", icon = "bolt", workloads = { web = { min = 2, max = 6, cpu_m = 200, cpu_limit_m = 400 } } }
    noche  = { name = "Noche", icon = "moon", workloads = { web = { min = 1, max = 1 } } }
  }
  weekly = [
    { days = ["mon", "tue", "wed", "thu", "fri"], from = 8, to = 20, profile = "normal" },
    { days = ["sat"], from = 10, to = 14, profile = "pico" },
  ]
`

func TestProgramacionCicloCompleto(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	ctx := context.Background()
	exc := futureDate(10)
	withExc := scheduleBody + fmt.Sprintf(`
  exceptions = {
    %q = { label = "Feriado", ranges = [{ from = 0, to = 24, profile = "pico" }] }
  }
`, exc)
	remote := func() client.ScheduleView {
		v, err := c.GetSchedule(ctx, "tienda", "prod")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: f.cfg(`
resource "bifrost_app" "tienda" {
  name = "tienda"
}
`)},
			{PreConfig: func() { f.srv.AddWorkload("tienda", "prod", "Deployment", "web", 2) }, Config: scheduleCfg(f, scheduleBody), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_schedule.prod", "id", "tienda/prod"),
				tfresource.TestCheckResourceAttr("bifrost_schedule.prod", "enabled", "true"),
				tfresource.TestCheckResourceAttr("bifrost_schedule.prod", "profiles.%", "3"),
				tfresource.TestCheckResourceAttr("bifrost_schedule.prod", "profiles.pico.workloads.web.cpu_m", "200"),
				func(*terraform.State) error {
					v := remote()
					// lunes 08–20 normal, sábado 10–14 pico, todo lo demás vacío (rige el base).
					if !v.Configured || v.Schedule.Weekly[0][8] != "normal" || v.Schedule.Weekly[0][19] != "normal" || v.Schedule.Weekly[0][20] != "" || v.Schedule.Weekly[5][10] != "pico" || v.Schedule.Weekly[5][14] != "" || v.Schedule.Weekly[6][12] != "" {
						return fmt.Errorf("matriz inesperada: %v", v.Schedule.Weekly)
					}
					return nil
				},
			)},
			{Config: scheduleCfg(f, scheduleBody), PlanOnly: true},
			// Excepción + horario cambiado: se actualiza en sitio.
			{Config: scheduleCfg(f, withExc), Check: func(*terraform.State) error {
				v := remote()
				if len(v.Schedule.Exceptions) != 1 || v.Schedule.Exceptions[0].Date != exc || v.Schedule.Exceptions[0].Hours[5] != "pico" {
					return fmt.Errorf("excepción: %+v", v.Schedule.Exceptions)
				}
				return nil
			}},
			{Config: scheduleCfg(f, withExc), PlanOnly: true},
			// Drift: alguien cambia el horario fuera de Terraform; el plan lo ve y el apply lo corrige.
			{PreConfig: func() {
				v := remote()
				s := v.Schedule
				s.Weekly[2][3] = "pico"
				if _, err := c.SetSchedule(ctx, "tienda", "prod", s); err != nil {
					t.Fatal(err)
				}
			}, Config: scheduleCfg(f, withExc), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: scheduleCfg(f, withExc), Check: func(*terraform.State) error {
				if remote().Schedule.Weekly[2][3] != "" {
					return fmt.Errorf("el apply no corrigió el drift")
				}
				return nil
			}},
			{ResourceName: "bifrost_schedule.prod", ImportState: true, ImportStateVerify: true},
			// Desactivar sin borrar.
			{Config: scheduleCfg(f, withExc+"\n  enabled = false\n"), Check: func(*terraform.State) error {
				if remote().Schedule.Enabled {
					return fmt.Errorf("debía quedar desactivada")
				}
				return nil
			}},
		},
		CheckDestroy: func(*terraform.State) error {
			v := remote()
			if v.Schedule.Enabled || !v.Configured || len(v.Schedule.Profiles) != 3 {
				return fmt.Errorf("destruir debe dejarla guardada y desactivada: %+v", v)
			}
			return nil
		},
	})
}

func TestProgramacionValidaciones(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	prof := `
  base = "noche"
  profiles = {
    noche = { name = "Noche", workloads = { web = { min = 1, max = 1 } } }
  }
`
	cases := map[string]struct{ body, err string }{
		"base inexistente":              {strings.Replace(prof, `"noche"`, `"dia"`, 1), `perfil base no existe`},
		"regla con perfil inexistente":  {prof + `weekly = [{ days = ["mon"], from = 1, to = 2, profile = "x" }]`, `no es un perfil`},
		"tramo al revés":                {prof + `weekly = [{ days = ["mon"], from = 10, to = 8, profile = "noche" }]`, `Tramo vacío|between 1 and 24|must be at least`},
		"día inexistente":               {prof + `weekly = [{ days = ["lunes"], from = 1, to = 2, profile = "noche" }]`, `must be one of`},
		"excepción vencida":             {prof + fmt.Sprintf(`exceptions = { %q = { ranges = [{ from = 0, to = 24, profile = "noche" }] } }`, futureDate(-30)), `Excepción vencida`},
		"excepción demasiado lejana":    {prof + fmt.Sprintf(`exceptions = { %q = { ranges = [{ from = 0, to = 24, profile = "noche" }] } }`, futureDate(500)), `demasiado lejana`},
		"id de perfil malo":             {strings.Replace(prof, "noche = {", `"Noche Mala" = {`, 1), `minúsculas, números y guiones`},
		"color en mayúsculas":           {strings.Replace(prof, `name = "Noche"`, `name = "Noche", color = "#2F6FDB"`, 1), `#rrggbb`},
		"icono desconocido":             {strings.Replace(prof, `name = "Noche"`, `name = "Noche", icon = "rayo"`, 1), `must be one of`},
		"mínimo 0 con máximo mayor":     {strings.Replace(prof, `min = 1, max = 1`, `min = 0, max = 2`, 1), `al menos 1`},
		"máximo menor que mínimo":       {strings.Replace(prof, `min = 1, max = 1`, `min = 3, max = 2`, 1), `no puede ser menor`},
		"límite menor que lo pedido":    {strings.Replace(prof, `min = 1, max = 1`, `min = 1, max = 1, cpu_m = 500, cpu_limit_m = 100`, 1), `no puede ser menor`},
		"nombre con espacios sobrantes": {strings.Replace(prof, `"Noche"`, `" Noche"`, 1), `Espacios sobrantes`},
		"nombres repetidos": {`
  base = "a"
  profiles = {
    a = { name = "Igual" }
    b = { name = "igual" }
  }`, `Nombre de perfil repetido`},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories,
				Steps: []tfresource.TestStep{{Config: scheduleCfg(f, tc.body), PlanOnly: true, ExpectError: regexp.MustCompile(tc.err)}}})
		})
	}
}

// Errores del servidor llegan al plan de forma clara: una carga que no existe.
func TestProgramacionCargaInexistente(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: scheduleCfg(f, scheduleBody), ExpectError: regexp.MustCompile(`(?s)la carga\s+«web»\s+no\s+existe`)},
	}})
}

// ---- escalada: schedule:manage abre SOLO la programación ----

func TestProgramacionConCuentaDePlataforma(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	adm := adminClient(t, f)
	ctx := context.Background()
	if _, err := adm.CreateApp(ctx, client.CreateAppReq{Name: "tienda-web", Owners: []string{"ana@example.org"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.CreateApp(ctx, client.CreateAppReq{Name: "secreta", Owners: []string{"ana@example.org"}}); err != nil {
		t.Fatal(err)
	}
	f.srv.AddWorkload("tienda-web", "prod", "Deployment", "web", 2)
	f.srv.AddWorkload("secreta", "prod", "Deployment", "web", 2)

	sched := func(name, app string) string {
		return fmt.Sprintf(`
resource "bifrost_schedule" %q {
  app         = %q
  environment = "prod"
  base        = "n"
  profiles    = { n = { name = "N", workloads = { web = { min = 1, max = 1 } } } }
}
`, name, app)
	}
	// Con schedule:manage sobre su prefijo: programa su App, no la ajena, y no abre ninguna otra cosa.
	t.Setenv("BIFROST_KEY_JSON", f.srv.CreatePlatformAccount("programadora", []string{"schedule:manage"}, nil, "tienda-", false))
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f.srv.URL)
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: prov + sched("p", "tienda-web"), Check: tfresource.TestCheckResourceAttr("bifrost_schedule.p", "id", "tienda-web/prod")},
		{Config: prov + sched("p", "tienda-web"), PlanOnly: true},
		{Config: prov + sched("p", "tienda-web") + sched("ajena", "secreta"), ExpectError: regexp.MustCompile(`(?s)404|No existe`)},
		{Config: prov + sched("p", "tienda-web") + `resource "bifrost_quota" "q" {
  app    = "tienda-web"
  cpu    = "4"
  memory = "8Gi"
  pods   = 40
  pvcs   = 10
}`, ExpectError: regexp.MustCompile(`(?s)403|no puede usar esa ruta`)},
		{Config: prov + sched("p", "tienda-web") + `resource "bifrost_scaling" "s" {
  app         = "tienda-web"
  environment = "prod"
  workload    = "web"
  horizontal  = { mode = "fixed", replicas = 2 }
}`, ExpectError: regexp.MustCompile(`(?s)403|no puede usar esa ruta`)},
	}})

	// Con casi todos los scopes menos schedule:manage no hay programación (apps:manage no la incluye).
	f2 := newFakeEnv(t)
	adm2 := adminClient(t, f2)
	if _, err := adm2.CreateApp(ctx, client.CreateAppReq{Name: "tienda-web", Owners: []string{"ana@example.org"}}); err != nil {
		t.Fatal(err)
	}
	f2.srv.AddWorkload("tienda-web", "prod", "Deployment", "web", 2)
	t.Setenv("BIFROST_KEY_JSON", f2.srv.CreatePlatformAccount("sin-programacion", []string{"apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage"}, nil, "tienda-", false, "example.org"))
	prov2 := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f2.srv.URL)
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: prov2 + sched("p", "tienda-web"), ExpectError: regexp.MustCompile(`(?s)403|no puede usar esa ruta`)},
	}})
}
