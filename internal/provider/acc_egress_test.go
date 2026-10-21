package provider

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

// ---- salidas a internet por workload ----

func egressCfg(f *fakeEnv, body string) string {
	return f.cfg(`
resource "bifrost_app" "tienda" {
  name = "tienda"
}
` + body)
}

const egressMail = `
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules = {
    smtp-out = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "mail" } }
  }
}
`

const egressMail2 = `
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules = {
    smtp-out   = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "mail" } }
    submission = { protocol = "tcp", port = 587, target = { kind = "statefulset", name = "mail" } }
  }
}
`

func TestSalidaCicloCompleto(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	seed := func() { f.srv.AddWorkload("tienda", "prod", "StatefulSet", "mail", 1) }
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: func(*terraform.State) error {
			if got := f.srv.EgressOf("tienda", "prod"); len(got) != 0 {
				return fmt.Errorf("destruir el recurso revoca todas las salidas, quedan %v", got)
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{Config: egressCfg(f, "")},
			{PreConfig: seed, Config: egressCfg(f, egressMail), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_egress.prod", "id", "tienda/prod"),
				tfresource.TestCheckResourceAttrSet("bifrost_egress.prod", "version"),
				tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.%", "1"),
				tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.smtp-out.port", "25"),
				tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.smtp-out.state", "applied"),
				tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.smtp-out.policy", "egress-smtp-out"),
				tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.smtp-out.target.name", "mail"),
				func(*terraform.State) error {
					if got := f.srv.EgressOf("tienda", "prod"); !reflect.DeepEqual(got, []string{"tcp/25 smtp-out"}) {
						return fmt.Errorf("salidas en el servidor: %v", got)
					}
					return nil
				},
			)},
			{Config: egressCfg(f, egressMail), PlanOnly: true},
			{Config: egressCfg(f, egressMail2), Check: tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.%", "2")},
			{Config: egressCfg(f, egressMail2), PlanOnly: true},
			{ResourceName: "bifrost_egress.prod", ImportState: true, ImportStateId: "tienda/prod", ImportStateVerify: true},
			// Deriva: alguien permite una salida fuera de Terraform; el plan la ve y el apply la revoca.
			{PreConfig: func() { f.srv.OpenEgressExternally("tienda", "prod", "rogue", 8081, "mail") }, Config: egressCfg(f, egressMail2), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: egressCfg(f, egressMail2), Check: func(*terraform.State) error {
				if got := f.srv.EgressOf("tienda", "prod"); !reflect.DeepEqual(got, []string{"tcp/25 smtp-out", "tcp/587 submission"}) {
					return fmt.Errorf("la deriva debe revocarse: %v", got)
				}
				return nil
			}},
			{Config: egressCfg(f, `
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules       = {}
}
`), Check: tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.%", "0")},
		},
	})
}

func TestSalidaErrores(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	ctx := context.Background()
	if _, err := c.CreateApp(ctx, client.CreateAppReq{Name: "blog", Owner: "jefa@example.org"}); err != nil {
		t.Fatal(err)
	}
	f.srv.AddWorkload("blog", "prod", "StatefulSet", "mail", 1)
	rule := func(name string, p int, proto string) string {
		return egressCfg(f, fmt.Sprintf(`
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules = {
    %s = { protocol = %q, port = %d, target = { kind = "statefulset", name = "mail" } }
  }
}
`, name, proto, p))
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: egressCfg(f, ""), Check: func(*terraform.State) error {
				f.srv.AddWorkload("tienda", "prod", "StatefulSet", "mail", 1)
				return nil
			}},
			{Config: rule("ssh", 22, "tcp"), ExpectError: regexp.MustCompile(`(?s)Puerto prohibido`)},
			{Config: rule("smb", 445, "tcp"), ExpectError: regexp.MustCompile(`(?s)Puerto prohibido`)},
			{Config: rule("kubelet", 10250, "tcp"), ExpectError: regexp.MustCompile(`(?s)Puerto prohibido`)},
			{Config: rule("np", 31000, "tcp"), ExpectError: regexp.MustCompile(`(?s)Puerto prohibido`)},
			{Config: rule("u", 123, "udp"), ExpectError: regexp.MustCompile(`(?s)tcp`)},
			{Config: rule("x_y", 25, "tcp"), ExpectError: regexp.MustCompile(`(?s)minúsculas, números y guiones`)},
			{Config: egressCfg(f, `
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules = {
    x = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "fantasma" } }
  }
}
`), ExpectError: regexp.MustCompile(`(?s)no\s+existe\s+en\s+el\s+entorno`)},
			{Config: egressCfg(f, `
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules = {
    a = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "mail" } }
    b = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "mail" } }
  }
}
`), ExpectError: regexp.MustCompile(`(?s)Regla repetida`)},
			{Config: egressCfg(f, `
resource "bifrost_egress" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  rules = {
    a = { protocol = "tcp", port = 8001, target = { kind = "statefulset", name = "mail" } }
    b = { protocol = "tcp", port = 8002, target = { kind = "statefulset", name = "mail" } }
    c = { protocol = "tcp", port = 8003, target = { kind = "statefulset", name = "mail" } }
    d = { protocol = "tcp", port = 8004, target = { kind = "statefulset", name = "mail" } }
    e = { protocol = "tcp", port = 8005, target = { kind = "statefulset", name = "mail" } }
    f = { protocol = "tcp", port = 8006, target = { kind = "statefulset", name = "mail" } }
  }
}
`), ExpectError: regexp.MustCompile(`(?s)at most 5|máximo|5`)},
			{Config: rule("ok", 25, "tcp"), Check: tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.ok.port", "25")},
		},
	})
	// Lo que el servidor rechaza aunque el plan no lo vea.
	_, err := c.SetEgress(ctx, "blog", "prod", client.SetEgressReq{Version: "antigua", Rules: []client.EgressSpec{}})
	if !client.IsConflict(err) || !versionConflict(err) {
		t.Errorf("versión vieja: %v", err)
	}
	if err := c.Call(ctx, "PUT", "/apps/blog/envs/prod/egress", nil, map[string]any{"version": ""}, nil); err == nil || !regexp.MustCompile(`Falta «rules»`).MatchString(err.Error()) {
		t.Errorf("«rules» es obligatorio: %v", err)
	}
	_, err = c.SetEgress(ctx, "blog", "prod", client.SetEgressReq{Rules: []client.EgressSpec{{Name: "ssh", Protocol: "tcp", Port: 22, Target: client.EgressTarget{Kind: "statefulset", Name: "mail"}}}})
	if err == nil || !regexp.MustCompile(`prohibido`).MatchString(err.Error()) {
		t.Errorf("el servidor rechaza los prohibidos: %v", err)
	}
	many := []client.EgressSpec{}
	for i := 0; i < 6; i++ {
		many = append(many, client.EgressSpec{Name: fmt.Sprintf("p%d", i), Protocol: "tcp", Port: int64(8000 + i), Target: client.EgressTarget{Kind: "statefulset", Name: "mail"}})
	}
	if _, err := c.SetEgress(ctx, "blog", "prod", client.SetEgressReq{Rules: many}); err == nil {
		t.Error("más de 5 reglas por entorno")
	}
}

// Escalada: ports:manage es el único scope que abre salidas por workload, solo en las Apps permitidas.
func TestSalidaMatrizDeEscalada(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	ctx := context.Background()
	for _, a := range []string{"tienda-web", "otra"} {
		if _, err := c.CreateApp(ctx, client.CreateAppReq{Name: a, Owner: "jefa@example.org"}); err != nil {
			t.Fatal(err)
		}
		f.srv.AddWorkload(a, "prod", "StatefulSet", "mail", 1)
	}
	body := func(app string) string {
		return fmt.Sprintf(`
resource "bifrost_egress" "prod" {
  app         = %q
  environment = "prod"
  rules = {
    smtp-out = { protocol = "tcp", port = 25, target = { kind = "statefulset", name = "mail" } }
  }
}
`, app)
	}
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f.srv.URL)
	otros := []string{"apps:read", "apps:create", "apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage", "queues:read", "queues:manage",
		"runners:read", "runners:manage", "replication:read", "replication:propose", "schedule:manage"}
	t.Setenv("BIFROST_KEY_JSON", f.srv.CreatePlatformAccount("sin-puertos", otros, nil, "tienda-", false, "example.org"))
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps:                    []tfresource.TestStep{{Config: prov + body("tienda-web"), ExpectError: regexp.MustCompile(`(?s)no\s+puede\s+usar\s+esa\s+ruta.*ports:manage`)}},
	})
	if got := f.srv.EgressOf("tienda-web", "prod"); len(got) != 0 {
		t.Fatalf("sin ports:manage no se abre nada: %v", got)
	}
	t.Setenv("BIFROST_KEY_JSON", f.srv.CreatePlatformAccount("salidas", []string{"ports:manage"}, nil, "tienda-", false))
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: func(*terraform.State) error {
			if got := f.srv.EgressOf("tienda-web", "prod"); len(got) != 0 {
				return fmt.Errorf("destroy revoca: %v", got)
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{Config: prov + body("tienda-web"), Check: tfresource.TestCheckResourceAttr("bifrost_egress.prod", "rules.smtp-out.state", "applied")},
			{Config: prov + body("tienda-web"), PlanOnly: true},
			{Config: prov + body("otra"), ExpectError: regexp.MustCompile(`(?s)No existe esa App`)},
		},
	})
	if got := f.srv.EgressOf("otra", "prod"); len(got) != 0 {
		t.Fatalf("la App ajena no cambia: %v", got)
	}
}
