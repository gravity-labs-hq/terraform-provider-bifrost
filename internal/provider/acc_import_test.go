package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

// Importar y planear debe dar 0 cambios y 0 reemplazos en TODOS los recursos importables (los atributos que fuerzan reemplazo se pueblan desde la API).
// Se prepara el estado "ya existente" con el cliente, se declaran los recursos como los escribiría un usuario (sin atributos que la API no devuelve)
// y se importan con bloques `import {}`; cada acción del plan debe ser no-op.
func TestImportarYPlanearDaCeroCambios(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	ctx := context.Background()
	c, err := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = c.CreateApp(ctx, client.CreateAppReq{Name: "tienda", Owners: []string{"ana@example.com"}, Envs: []client.CreateEnvReq{{Name: "prod"}}})
	must(err)
	_, err = c.CreateApp(ctx, client.CreateAppReq{Name: "pagos", Owners: []string{"ana@example.com"}, Envs: []client.CreateEnvReq{{Name: "prod"}}})
	must(err)
	_, err = c.CreateEnv(ctx, "tienda", client.CreateEnvReq{Name: "dev", Quota: &client.Quota{CPU: "1", Memory: "2Gi", Pods: 10, PVCs: 2}})
	must(err)
	_, err = c.AttachService(ctx, "tienda", "prod", client.AttachServiceReq{Service: "postgres", Options: map[string]string{"version": "16"}})
	must(err)
	_, err = c.CreateAccount(ctx, "tienda", client.CreateAccountReq{Name: "ci", Role: "readonly", Envs: []string{"prod"}})
	must(err)
	_, err = c.AddMember(ctx, "tienda", "marta@example.com", "deploy")
	must(err)
	b, err := c.CreateBinding(ctx, "tienda", client.CreateBindingReq{Kind: "gitlab", ProjectPath: "acme/tienda", Role: "deploy", RefProtected: true, Claims: map[string]string{"environment": "production"}, Envs: []string{"prod"}})
	must(err)
	_, err = c.SetNetwork(ctx, "tienda", "prod", client.NetworkReq{Domains: []string{"www.tienda.cl"}, Internet: true, InternetPorts: []int64{443}, Egress: []client.Egress{}})
	must(err)
	_, err = c.SetNetwork(ctx, "tienda", "dev", client.NetworkReq{Domains: []string{}, InternetPorts: []int64{}, Egress: []client.Egress{{CIDR: "10.0.0.0/24", Port: 5432, Reason: "base"}}})
	must(err)
	_, err = c.SetQuota(ctx, "tienda", client.Quota{CPU: "4", Memory: "8Gi", Pods: 40, PVCs: 10})
	must(err)
	_, err = c.SetHostRules(ctx, "tienda", "prod", "www.tienda.cl", client.HostRules{IPAllow: []string{}, IPDeny: []string{"203.0.113.0/24"}, SecurityHeaders: true, BasicAuth: []client.BasicUser{}})
	must(err)
	must(c.AllowAccess(ctx, "tienda", "prod", client.AllowAccessReq{From: "pagos", Service: "api", Port: 8080}))
	f.srv.AddWorkload("tienda", "prod", "Deployment", "web", 2)
	_, err = c.CreateQueue(ctx, client.CreateQueueReq{Name: "mails", Display: "Correos", Kind: "bullmq"})
	must(err)
	_, err = c.CreateQueue(ctx, client.CreateQueueReq{Name: "ev", Kind: "jetstream", JetStream: &client.JetStreamSpec{Subjects: []string{"ev.>"}, Retention: "workqueue", Replicas: 1, Storage: "file", MaxAge: 3600}})
	must(err)
	mx, mn := int64(6), int64(1)
	_, err = c.CreateRunnerPool(ctx, client.CreateRunnerPoolReq{Name: "ci", Labels: []string{"docker"}, Origins: []string{"own"}, Min: &mn, Max: &mx, App: "tienda"})
	must(err)
	_, err = c.AddAdmin(ctx, "ops@example.com")
	must(err)
	_, err = c.CreatePlatformAccount(ctx, client.CreatePlatformAccountReq{Name: "terraform", Scopes: []string{"apps:manage"}, Apps: []string{"tienda"}, ExpiresInDays: 30})
	must(err)

	type res struct{ addr, typ, id, body string }
	rs := []res{
		{"app.t", "bifrost_app", "tienda", `name = "tienda"`},
		{"environment.prod", "bifrost_environment", "tienda/prod", `app = "tienda"
  name = "prod"`},
		{"environment.dev", "bifrost_environment", "tienda/dev", `app = "tienda"
  name = "dev"`},
		{"service.pg", "bifrost_service", "tienda/prod/postgres", `app = "tienda"
  environment = "prod"
  service = "postgres"`},
		{"service_account.ci", "bifrost_service_account", "tienda/ci", `app = "tienda"
  name = "ci"
  role = "readonly"
  environments = ["prod"]`},
		{"member.m", "bifrost_member", "tienda/marta@example.com", `app = "tienda"
  email = "marta@example.com"
  role = "deploy"`},
		{"binding.b", "bifrost_binding", "tienda/" + b.ID, `app = "tienda"
  project_path = "acme/tienda"
  role = "deploy"
  ref_protected = true
  claims = { environment = "production" }
  environments = ["prod"]`},
		{"network.prod", "bifrost_network", "tienda/prod", `app = "tienda"
  environment = "prod"
  domains = ["www.tienda.cl"]
  internet = true
  internet_ports = [443]`},
		{"network.dev", "bifrost_network", "tienda/dev", `app = "tienda"
  environment = "dev"
  egress = [{ cidr = "10.0.0.0/24", port = 5432, reason = "base" }]`},
		{"quota.q", "bifrost_quota", "tienda", `app = "tienda"
  cpu = "4"
  memory = "8Gi"
  pods = 40
  pvcs = 10`},
		{"host_rules.h", "bifrost_host_rules", "tienda/prod/www.tienda.cl", `app = "tienda"
  environment = "prod"
  host = "www.tienda.cl"
  ip_deny = ["203.0.113.0/24"]
  security_headers = true`},
		{"access.a", "bifrost_access", "tienda/prod/pagos/api/8080", `app = "tienda"
  environment = "prod"
  from = "pagos"
  service = "api"
  port = 8080`},
		{"scaling.s", "bifrost_scaling", "tienda/prod/Deployment/web", `app = "tienda"
  environment = "prod"
  workload = "web"
  horizontal = { mode = "fixed", replicas = 2 }`},
		{"queue.mails", "bifrost_queue", "bullmq/mails", `name = "mails"
  display = "Correos"`},
		{"queue.ev", "bifrost_queue", "jetstream/ev", `kind = "jetstream"
  name = "ev"
  jetstream = { subjects = ["ev.>"], retention = "workqueue", replicas = 1, storage = "file", max_age_seconds = 3600 }`},
		{"runner_pool.ci", "bifrost_runner_pool", "ci", `name = "ci"
  labels = ["docker"]
  origins = ["own"]
  min = 1
  max = 6
  app = "tienda"`},
		{"admin.ops", "bifrost_admin", "ops@example.com", `email = "ops@example.com"`},
	}
	var cfg, cfgOK strings.Builder
	var checks []plancheck.PlanCheck
	for _, r := range rs {
		name := strings.SplitN(r.addr, ".", 2)[1]
		full := "bifrost_" + strings.TrimPrefix(r.typ, "bifrost_") + "." + name
		fmt.Fprintf(&cfg, "resource %q %q {\n  %s\n}\nimport {\n  to = %s\n  id = %q\n}\n", r.typ, name, r.body, full, r.id)
		body := r.body
		if r.typ == "bifrost_service" || r.addr == "queue.ev" {
			body += "\n  confirm_data_loss = true" // protección contra pérdida de datos: hay que declararla para poder destruir
		}
		fmt.Fprintf(&cfgOK, "resource %q %q {\n  %s\n}\n", r.typ, name, body)
		checks = append(checks, plancheck.ExpectResourceAction(full, plancheck.ResourceActionNoop))
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			// El plan con los import{} no debe proponer cambios ni reemplazos; tras aplicarlo (solo estado) el plan vuelve a quedar vacío.
			{Config: f.cfg(cfg.String()), ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: checks}},
			{Config: f.cfg(cfg.String()), PlanOnly: true},
			// Declarar la protección de datos es un cambio solo de estado (en sitio, sin reemplazos) y permite destruir al final.
			{Config: f.cfg(cfgOK.String()), ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("bifrost_service.pg", plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction("bifrost_queue.ev", plancheck.ResourceActionUpdate)}}},
		},
	})
}

func importedPlatform(t *testing.T) (*fakeEnv, func(body string) string) {
	t.Helper()
	cleanEnv(t)
	f := newFakeEnv(t)
	c, _ := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if _, err := c.CreatePlatformAccount(context.Background(), client.CreatePlatformAccountReq{Name: "terraform", Scopes: []string{"apps:manage"}, Apps: []string{"tienda"}, ExpiresInDays: 30}); err != nil {
		t.Fatal(err)
	}
	return f, func(extra string) string {
		return f.cfg(`resource "bifrost_platform_account" "t" {
  name   = "terraform"
  scopes = ["apps:manage"]
  apps   = ["tienda"]
` + extra + `
}
import {
  to = bifrost_platform_account.t
  id = "terraform"
}`)
	}
}

func rotations(f *fakeEnv) int {
	return f.srv.RequestCount("POST /v1/platform-accounts/terraform/keys")
}

// Importar y declarar expires_in_days igual a lo que le queda a la cuenta, sin keepers: adopta (solo estado) y NO rota; no se afirma ninguna rotación.
func TestImportarCuentaDePlataformaSinCambioEfectivoNoRota(t *testing.T) {
	f, cfg := importedPlatform(t)
	c := cfg("  expires_in_days = 30")
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: c, ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("bifrost_platform_account.t", plancheck.ResourceActionUpdate)}}},
		{Config: c, PlanOnly: true},
	}})
	if n := rotations(f); n != 0 {
		t.Fatalf("sin cambio efectivo no debe rotar (%d)", n)
	}
}

// Un keepers declarado tras importar SIEMPRE rota (es un valor nuevo frente al centinela), y los siguientes cambios también; sin cambio de valor no rota otra vez.
func TestImportarCuentaDePlataformaKeepersSiempreRota(t *testing.T) {
	f, cfg := importedPlatform(t)
	c := func(k string) string { return cfg("  expires_in_days = 30\n  keepers = { rotacion = \"" + k + "\" }") }
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: c("a"), Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.t", "key_id", "k2")},
		{Config: c("a"), PlanOnly: true},
		{Config: c("b"), Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.t", "key_id", "k3")},
	}})
	if n := rotations(f); n != 2 {
		t.Fatalf("esperaba 2 rotaciones (primer keepers y su cambio), hubo %d", n)
	}
	st := f.srv.PlatformKeyState("terraform")
	if !st["k3"] {
		t.Fatalf("la clave nueva debe valer: %v", st)
	}
}

// Cambiar expires_in_days tras importar (distinto de lo efectivo) rota; igual dentro de ±1 día no.
func TestImportarCuentaDePlataformaExpiresDistintoRota(t *testing.T) {
	f, cfg := importedPlatform(t)
	c := cfg("  expires_in_days = 90")
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: c, Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.t", "key_id", "k2")},
		{Config: c, PlanOnly: true},
	}})
	if n := rotations(f); n != 1 {
		t.Fatalf("rotaciones: %d", n)
	}
}

// Clave vencida / revocada / inexistente: el plan avisa y el apply ROTA aunque el usuario no haya cambiado nada (importada o creada por Terraform).
func TestCuentaDePlataformaClaveVencidaSeRota(t *testing.T) {
	f, cfg := importedPlatform(t)
	f.srv.ExpireKeysOutside("terraform") // la clave de la cuenta importada ya venció
	c := cfg("  expires_in_days = 30")
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: c, ConfigPlanChecks: tfresource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("bifrost_platform_account.t", plancheck.ResourceActionUpdate)}},
			Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.t", "key_id", "k2")},
		{Config: c, PlanOnly: true},
		// Revocada por fuera después: el siguiente plan vuelve a proponer rotar y el apply la rota.
		{PreConfig: func() { f.srv.RevokeKeysOutside("terraform") }, Config: c, PlanOnly: true, ExpectNonEmptyPlan: true},
		{Config: c, Check: tfresource.TestCheckResourceAttr("bifrost_platform_account.t", "key_id", "k3")},
	}})
	if n := rotations(f); n != 2 {
		t.Fatalf("rotaciones: %d", n)
	}
	st := f.srv.PlatformKeyState("terraform")
	if !st["k3"] || st["k1"] || st["k2"] {
		t.Fatalf("solo la última clave debe valer (k1 venció, k2 revocada): %v", st)
	}
}

// Criterio de rotación: cualquier dato desconocido, vacío, ilegible, vencido o por vencer rota; solo lo claramente sano no.
func TestCriterioDeRotacionFallaCerrado(t *testing.T) {
	now := time.Now()
	ok := now.Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	good := func(m *platformAccountModel) {
		m.KeyExpiresAt, m.ExpiresAt = types.StringValue(ok), types.StringValue(ok)
		m.Keepers = types.MapValueMust(types.StringType, map[string]attr.Value{"r": types.StringValue("a")})
		m.ExpiresInDays = types.Int64Value(30)
	}
	var st, plan platformAccountModel
	good(&st)
	good(&plan)
	if rot, why := shouldRotate(st, plan, now); rot {
		t.Fatalf("lo sano no rota: %s", why)
	}
	bad := map[string]func(*platformAccountModel, *platformAccountModel){
		"clave nula":        func(s, p *platformAccountModel) { s.KeyExpiresAt = types.StringNull() },
		"clave desconocida": func(s, p *platformAccountModel) { s.KeyExpiresAt = types.StringUnknown() },
		"sin clave vigente": func(s, p *platformAccountModel) { s.KeyExpiresAt = types.StringValue("") },
		"vigencia ilegible": func(s, p *platformAccountModel) { s.KeyExpiresAt = types.StringValue("mañana") },
		"clave vencida": func(s, p *platformAccountModel) {
			s.KeyExpiresAt = types.StringValue(now.Add(-time.Hour).UTC().Format(time.RFC3339))
		},
		"clave por vencer": func(s, p *platformAccountModel) {
			s.KeyExpiresAt = types.StringValue(now.Add(24 * time.Hour).UTC().Format(time.RFC3339))
		},
		"cuenta ilegible": func(s, p *platformAccountModel) { s.ExpiresAt = types.StringValue("x") },
		"cuenta por vencer": func(s, p *platformAccountModel) {
			s.ExpiresAt = types.StringValue(now.Add(time.Hour).UTC().Format(time.RFC3339))
		},
		"keepers cambió": func(s, p *platformAccountModel) {
			p.Keepers = types.MapValueMust(types.StringType, map[string]attr.Value{"r": types.StringValue("b")})
		},
		"keepers desconocido": func(s, p *platformAccountModel) { p.Keepers = types.MapUnknown(types.StringType) },
		"primer keepers":      func(s, p *platformAccountModel) { s.Keepers = importedKeepers() },
		"expires cambió":      func(s, p *platformAccountModel) { p.ExpiresInDays = types.Int64Value(60) },
		"expires nulo":        func(s, p *platformAccountModel) { p.ExpiresInDays = types.Int64Null() },
		"importada y distinta": func(s, p *platformAccountModel) {
			s.ExpiresInDays = types.Int64Null()
			p.ExpiresInDays = types.Int64Value(90)
		},
		"importada y caducidad ilegible": func(s, p *platformAccountModel) {
			s.ExpiresInDays = types.Int64Null()
			s.ExpiresAt = types.StringValue("?")
		},
	}
	for name, mod := range bad {
		s, p := st, plan
		mod(&s, &p)
		if rot, _ := shouldRotate(s, p, now); !rot {
			t.Errorf("%s: debía rotar (fail-closed)", name)
		}
	}
	// Importada con el valor efectivo (±1 día): no rota.
	s, p := st, plan
	s.ExpiresInDays = types.Int64Null()
	s.Keepers = importedKeepers()
	p.Keepers = importedKeepers()
	p.ExpiresInDays = types.Int64Value(30)
	if rot, why := shouldRotate(s, p, now); rot {
		t.Errorf("importada con el valor efectivo no rota: %s", why)
	}
}
