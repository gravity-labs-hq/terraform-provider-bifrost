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

// ---- puertos públicos TCP/UDP ----

func portsCfg(f *fakeEnv, body string) string {
	return f.cfg(`
resource "bifrost_app" "tienda" {
  name = "tienda"
}
` + body)
}

// Correo de un servidor Stalwart: SMTP y submission abiertos a todos, IMAPS solo desde un rango.
const portsMail = `
resource "bifrost_ports" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  ports = {
    smtp  = { protocol = "tcp", port = 25, target = { kind = "deployment", name = "stalwart", port = 25 } }
    imap  = { protocol = "tcp", port = 143, target = { kind = "deployment", name = "stalwart", port = 143 } }
    imaps = { protocol = "tcp", port = 993, target = { kind = "deployment", name = "stalwart", port = 993 }, allow_cidrs = ["203.0.113.0/24", "198.51.100.7/32"] }
  }
}
`

const portsMail2 = `
resource "bifrost_ports" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  ports = {
    smtp       = { protocol = "tcp", port = 25, target = { kind = "deployment", name = "stalwart", port = 25 } }
    submission = { protocol = "tcp", port = 587, target = { kind = "deployment", name = "stalwart", port = 587 } }
    imaps      = { protocol = "tcp", port = 993, target = { kind = "deployment", name = "stalwart", port = 993 }, allow_cidrs = ["203.0.113.0/24"] }
  }
}
`

func TestPuertosCicloCompleto(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	seed := func() { f.srv.AddWorkload("tienda", "prod", "Deployment", "stalwart", 1) }
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: func(*terraform.State) error {
			if got := f.srv.PortsOf("tienda", "prod"); len(got) != 0 {
				return fmt.Errorf("destruir el recurso cierra todos los puertos, quedan %v", got)
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{Config: portsCfg(f, "")},
			// Crear: SMTP, IMAP e IMAPS.
			{PreConfig: seed, Config: portsCfg(f, portsMail), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "id", "tienda/prod"),
				tfresource.TestCheckResourceAttrSet("bifrost_ports.prod", "version"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.%", "3"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.port", "25"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.state", "open"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.lb_ready", "true"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.service", "bfp-smtp"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.allow_cidrs.#", "0"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.addresses.#", "1"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.imaps.allow_cidrs.#", "2"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.imaps.target.name", "stalwart"),
				func(*terraform.State) error {
					if got := f.srv.PortsOf("tienda", "prod"); !reflect.DeepEqual(got, []string{"tcp/143 imap", "tcp/25 smtp", "tcp/993 imaps"}) {
						return fmt.Errorf("puertos en el servidor: %v", got)
					}
					return nil
				},
			)},
			// Sin cambios: el plan queda vacío.
			{Config: portsCfg(f, portsMail), PlanOnly: true},
			// Actualizar: quita imap, añade submission y cambia los rangos de imaps.
			{Config: portsCfg(f, portsMail2), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.%", "3"),
				tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.imaps.allow_cidrs.#", "1"),
				func(*terraform.State) error {
					if got := f.srv.PortsOf("tienda", "prod"); !reflect.DeepEqual(got, []string{"tcp/25 smtp", "tcp/587 submission", "tcp/993 imaps"}) {
						return fmt.Errorf("puertos en el servidor: %v", got)
					}
					return nil
				},
			)},
			{Config: portsCfg(f, portsMail2), PlanOnly: true},
			// Importar por app/entorno.
			{ResourceName: "bifrost_ports.prod", ImportState: true, ImportStateId: "tienda/prod", ImportStateVerify: true},
			// Deriva: alguien abre un puerto fuera de Terraform; el plan lo ve y el apply lo cierra (el recurso es autoritativo).
			{PreConfig: func() { f.srv.OpenPortExternally("tienda", "prod", "rogue", "tcp", 8081, "stalwart") }, Config: portsCfg(f, portsMail2), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: portsCfg(f, portsMail2), Check: func(*terraform.State) error {
				if got := f.srv.PortsOf("tienda", "prod"); !reflect.DeepEqual(got, []string{"tcp/25 smtp", "tcp/587 submission", "tcp/993 imaps"}) {
					return fmt.Errorf("la deriva debe cerrarse: %v", got)
				}
				return nil
			}},
			// Vaciar la lista cierra todo sin destruir el recurso.
			{Config: portsCfg(f, `
resource "bifrost_ports" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  ports       = {}
}
`), Check: tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.%", "0")},
		},
	})
}

func TestPuertosErrores(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	ctx := context.Background()
	if _, err := c.CreateApp(ctx, client.CreateAppReq{Name: "blog", Owner: "jefa@example.org"}); err != nil {
		t.Fatal(err)
	}
	f.srv.AddWorkload("blog", "prod", "Deployment", "stalwart", 1)
	// Otra App ya tiene tcp/25.
	if _, err := c.SetPorts(ctx, "blog", "prod", client.SetPortsReq{Ports: []client.PortSpec{{Name: "smtp", Protocol: "tcp", Port: 25, Target: client.PortTarget{Kind: "deployment", Name: "stalwart", Port: 25}}}}); err != nil {
		t.Fatal(err)
	}
	port := func(name string, p int, extra string) string {
		return portsCfg(f, fmt.Sprintf(`
resource "bifrost_ports" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  ports = {
    %s = { protocol = "tcp", port = %d, target = { kind = "deployment", name = "stalwart", port = 25 }%s }
  }
}
`, name, p, extra))
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: portsCfg(f, ""), Check: func(*terraform.State) error {
				f.srv.AddWorkload("tienda", "prod", "Deployment", "stalwart", 1)
				return nil
			}},
			// Reservados (SSH, web, GitLab SSH, rango NodePort): fallan en el plan.
			{Config: port("ssh", 22, ""), ExpectError: regexp.MustCompile(`(?s)Puerto reservado`)},
			{Config: port("web", 443, ""), ExpectError: regexp.MustCompile(`(?s)Puerto reservado`)},
			{Config: port("np", 31000, ""), ExpectError: regexp.MustCompile(`(?s)Puerto reservado`)},
			// Nombres hostiles, protocolo, rangos no canónicos o internos.
			{Config: port("x_y", 2500, ""), ExpectError: regexp.MustCompile(`(?s)minúsculas, números y guiones`)},
			{Config: port("x", 2500, `, allow_cidrs = ["203.0.113.9"]`), ExpectError: regexp.MustCompile(`(?s)CIDR\s+canónico`)},
			{Config: port("x", 2500, `, allow_cidrs = ["10.0.0.0/8"]`), ExpectError: regexp.MustCompile(`(?s)interno`)},
			// Colisión con otra App (409): se muestra claro y no se reintenta.
			{Config: port("smtp", 25, ""), ExpectError: regexp.MustCompile(`(?s)Puerto\s+en\s+uso.*ya\s+está\s+reservado`)},
			// Workload inexistente.
			{Config: portsCfg(f, `
resource "bifrost_ports" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  ports = {
    x = { protocol = "tcp", port = 2500, target = { kind = "deployment", name = "fantasma", port = 25 } }
  }
}
`), ExpectError: regexp.MustCompile(`(?s)no\s+existe\s+en\s+el\s+entorno`)},
			// Repetido: dos nombres con el mismo protocolo y puerto.
			{Config: portsCfg(f, `
resource "bifrost_ports" "prod" {
  app         = bifrost_app.tienda.name
  environment = "prod"
  ports = {
    a = { protocol = "tcp", port = 2500, target = { kind = "deployment", name = "stalwart", port = 25 } }
    b = { protocol = "tcp", port = 2500, target = { kind = "deployment", name = "stalwart", port = 25 } }
  }
}
`), ExpectError: regexp.MustCompile(`(?s)Puerto repetido`)},
			// Una opción válida sigue funcionando después de tanto rechazo.
			{Config: port("ok", 2500, ""), Check: tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.ok.port", "2500")},
		},
	})
	// Lo que rechaza el servidor aunque el plan no lo vea: reservado ampliable, versión vieja, cuerpo sin «ports», tope por App.
	_, err := c.SetPorts(ctx, "blog", "prod", client.SetPortsReq{Version: "antigua", Ports: []client.PortSpec{}})
	if !client.IsConflict(err) || !versionConflict(err) {
		t.Errorf("versión vieja: %v", err)
	}
	_, err = c.SetPorts(ctx, "blog", "prod", client.SetPortsReq{Version: "", Ports: []client.PortSpec{{Name: "ssh", Protocol: "tcp", Port: 22, Target: client.PortTarget{Kind: "deployment", Name: "stalwart", Port: 22}}}})
	if err == nil || !regexp.MustCompile(`reservado`).MatchString(err.Error()) {
		t.Errorf("el servidor rechaza los reservados: %v", err)
	}
	if err := c.Call(ctx, "PUT", "/apps/blog/envs/prod/ports", nil, map[string]any{"version": ""}, nil); err == nil || !regexp.MustCompile(`Falta «ports»`).MatchString(err.Error()) {
		t.Errorf("«ports» es obligatorio: %v", err)
	}
	many := []client.PortSpec{}
	for i := 0; i < 11; i++ {
		many = append(many, client.PortSpec{Name: fmt.Sprintf("p%d", i), Protocol: "udp", Port: int64(3000 + i), Target: client.PortTarget{Kind: "deployment", Name: "stalwart", Port: 1}})
	}
	if _, err := c.SetPorts(ctx, "blog", "prod", client.SetPortsReq{Version: "", Ports: many}); err == nil {
		t.Error("más de 10 puertos por App")
	}
	if versionConflict(fmt.Errorf("x")) {
		t.Error("un error cualquiera no es conflicto de versión")
	}
}

// Matriz de escalada del provider: ports:manage es el único scope que abre los puertos, solo en las Apps permitidas.
func TestPuertosMatrizDeEscalada(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	c := adminClient(t, f)
	ctx := context.Background()
	for _, a := range []string{"tienda-web", "otra"} {
		if _, err := c.CreateApp(ctx, client.CreateAppReq{Name: a, Owner: "jefa@example.org"}); err != nil {
			t.Fatal(err)
		}
		f.srv.AddWorkload(a, "prod", "Deployment", "stalwart", 1)
	}
	body := func(app string) string {
		return fmt.Sprintf(`
resource "bifrost_ports" "prod" {
  app         = %q
  environment = "prod"
  ports = {
    smtp = { protocol = "tcp", port = 25, target = { kind = "deployment", name = "stalwart", port = 25 } }
  }
}
`, app)
	}
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f.srv.URL)

	// Todos los demás scopes juntos no abren un puerto: 403 claro.
	otros := []string{"apps:read", "apps:create", "apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage", "queues:read", "queues:manage",
		"runners:read", "runners:manage", "replication:read", "replication:propose", "schedule:manage"}
	t.Setenv("BIFROST_KEY_JSON", f.srv.CreatePlatformAccount("sin-puertos", otros, nil, "tienda-", false, "example.org"))
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: prov + body("tienda-web"), ExpectError: regexp.MustCompile(`(?s)no\s+puede\s+usar\s+esa\s+ruta.*ports:manage`)},
		},
	})
	if got := f.srv.PortsOf("tienda-web", "prod"); len(got) != 0 {
		t.Fatalf("sin ports:manage no se abre nada: %v", got)
	}

	// Con ports:manage: su App sí; la ajena no existe para ella (404) y no cambia nada.
	t.Setenv("BIFROST_KEY_JSON", f.srv.CreatePlatformAccount("puertos", []string{"ports:manage"}, nil, "tienda-", false))
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		CheckDestroy: func(*terraform.State) error {
			if got := f.srv.PortsOf("tienda-web", "prod"); len(got) != 0 {
				return fmt.Errorf("destroy cierra: %v", got)
			}
			return nil
		},
		Steps: []tfresource.TestStep{
			{Config: prov + body("tienda-web"), Check: tfresource.TestCheckResourceAttr("bifrost_ports.prod", "ports.smtp.state", "open")},
			{Config: prov + body("tienda-web"), PlanOnly: true},
			{Config: prov + body("otra"), ExpectError: regexp.MustCompile(`(?s)No existe esa App`)},
		},
	})
	if got := f.srv.PortsOf("otra", "prod"); len(got) != 0 {
		t.Fatalf("la App ajena no cambia: %v", got)
	}

	// ports:manage no se obtiene por ningún otro scope: ni siquiera una cuenta con el scope puede crear otra con él (solo una persona administradora).
	t.Setenv("BIFROST_KEY_JSON", f.srv.CreatePlatformAccount("puertos2", []string{"ports:manage"}, nil, "tienda-", false))
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: prov + `resource "bifrost_platform_account" "copia" {
  name            = "copia"
  scopes          = ["ports:manage"]
  allow_ports     = true
  app_prefix      = "tienda-"
  expires_in_days = 5
}`, ExpectError: regexp.MustCompile(`Esta\s+cuenta\s+de\s+plataforma\s+no\s+puede\s+usar\s+esa\s+ruta`)},
		},
	})
}

// El administrador crea la cuenta de plataforma con ports:manage: opt-in y límite obligatorios, validados en el plan y en el servidor.
func TestCuentaDePlataformaPuertos(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	acc := func(extra string) string {
		return f.cfg(`resource "bifrost_platform_account" "puertos" {
  name            = "puertos"
  expires_in_days = 30
` + extra + `
}`)
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: acc(`scopes = ["ports:manage"]`), ExpectError: regexp.MustCompile(`(?s)ports:manage es opt-in`)},
			{Config: acc("scopes = [\"ports:manage\"]\nallow_ports = true"), ExpectError: regexp.MustCompile(`(?s)exige un límite`)},
			{Config: acc("scopes = [\"apps:read\"]\nallow_ports = true"), ExpectError: regexp.MustCompile(`(?s)solo tiene sentido`)},
			{Config: acc("scopes = [\"ports:manage\"]\nallow_ports = true\napp_prefix = \"tienda-\""), Check: tfresource.ComposeTestCheckFunc(
				tfresource.TestCheckResourceAttr("bifrost_platform_account.puertos", "allow_ports", "true"),
				tfresource.TestCheckResourceAttr("bifrost_platform_account.puertos", "effective_scopes.#", "2"), // ports:manage + apps:read
			)},
			{Config: acc("scopes = [\"ports:manage\"]\nallow_ports = true\napp_prefix = \"tienda-\""), PlanOnly: true},
		},
	})
	// El servidor tampoco lo concede sin opt-in aunque el plan no lo viera.
	c := adminClient(t, f)
	_, err := c.CreatePlatformAccount(context.Background(), client.CreatePlatformAccountReq{Name: "xx", Scopes: []string{"ports:manage"}, AppPrefix: "tienda-", ExpiresInDays: 5, AllowedDomains: []string{}})
	if err == nil || !regexp.MustCompile(`opt-in`).MatchString(err.Error()) {
		t.Errorf("sin opt-in: %v", err)
	}
}
