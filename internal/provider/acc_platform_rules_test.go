package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

func platAcct(extra string) string {
	return `resource "bifrost_platform_account" "t" {
  name            = "terraform"
  expires_in_days = 30
` + extra + "\n}"
}

// Reglas de creación de la cuenta de plataforma (las valida el servidor y el provider las traduce).
func TestCuentaDePlataformaReglasDeCreacion(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	cases := map[string]struct{ body, err string }{
		"apps:create exige prefijo": {`scopes = ["apps:create"]` + "\n allowed_domains = [\"example.org\"]", `apps:create exige\s+app_prefix`},
		"identities es opt-in":      {`scopes = ["identities:manage"]` + "\n app_prefix = \"tienda-\"", `opt-in`},
		"identities exige límite":   {`scopes = ["identities:manage"]` + "\n allow_identities = true", `nunca sin\s+límite`},
		"owners exigen dominios":    {`scopes = ["apps:create"]` + "\n app_prefix = \"tienda-\"", `allowed_domains`},
		"colas exigen prefijo":      {`scopes = ["queues:manage"]`, `app_prefix`},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories,
				Steps: []tfresource.TestStep{{Config: f.cfg(platAcct(tc.body)), ExpectError: regexp.MustCompile(tc.err)}}})
		})
	}
	// Las cuentas con scopes sensibles funcionan de inmediato al crearlas (sin aprobación): se crean y se pueden usar.
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{{
		Config: f.cfg(platAcct(`scopes = ["identities:manage", "members:manage", "replication:propose"]
  allow_identities = true
  allowed_domains  = ["example.org"]
  app_prefix       = "tienda-"`)),
		Check: tfresource.ComposeTestCheckFunc(
			tfresource.TestCheckResourceAttr("bifrost_platform_account.t", "allow_identities", "true"),
			tfresource.TestCheckResourceAttrSet("bifrost_platform_account.t", "key_json"),
		)}}})
}

// Con la cuenta de plataforma: owners fuera de allowed_domains, cuentas de servicio sin entornos y restaurar fallan con mensajes claros.
func TestCuentaDePlataformaLimitesYRestaurar(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	key := f.srv.CreatePlatformAccount("terraform", []string{"apps:create", "apps:manage", "identities:manage"}, nil, "tienda-", false, "example.org")
	t.Setenv("BIFROST_KEY_JSON", key)
	prov := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f.srv.URL)
	app := `resource "bifrost_app" "web" {
  name   = "tienda-web"
  owners = ["ana@example.org"]
}
`
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{
		{Config: prov + app},
		{Config: prov + app + `resource "bifrost_app" "malo" {
  name   = "tienda-malo"
  owners = ["ana@otro.com"]
}`, ExpectError: regexp.MustCompile(`fuera de los\s+dominios permitidos`)},
		{Config: prov + app + `resource "bifrost_service_account" "ci" {
  app  = bifrost_app.web.name
  name = "ci"
  role = "deploy"
}`, ExpectError: regexp.MustCompile(`debe indicar los\s+entornos`)},
		{Config: prov + app + `resource "bifrost_service_account" "ci" {
  app          = bifrost_app.web.name
  name         = "ci"
  role         = "deploy"
  environments = ["prod"]
}`, Check: tfresource.TestCheckResourceAttr("bifrost_service_account.ci", "expires_in_days", "7")}, // key_ttl corto por omisión
		{Config: prov + app + `resource "bifrost_binding" "b" {
  app          = bifrost_app.web.name
  project_path = "acme/tienda"
  role         = "deploy"
  environments = ["prod"]
}`, ExpectError: regexp.MustCompile(`ref_protected`)},
		{Config: prov + app, Check: tfresource.TestCheckResourceAttr("bifrost_app.web", "managed_by", "terraform")},
	}})
	// restore_archived con una cuenta de servicio: error claro con el flujo manual.
	f2 := newFakeEnv(t)
	admin, _ := client.New(client.Config{API: f2.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	ctx := context.Background()
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: "tienda-vieja", Owners: []string{"a@example.org"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ArchiveApp(ctx, "tienda-vieja"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BIFROST_KEY_JSON", f2.srv.CreatePlatformAccount("terraform", []string{"apps:create", "apps:manage"}, nil, "tienda-", false, "example.org"))
	prov2 := fmt.Sprintf("provider \"bifrost\" {\n  api = %q\n}\n", f2.srv.URL)
	tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories, Steps: []tfresource.TestStep{{
		Config: prov2 + `resource "bifrost_app" "v" {
  name             = "tienda-vieja"
  owners           = ["a@example.org"]
  restore_archived = true
}`, ExpectError: regexp.MustCompile(`(?s)solo de una\s+persona\s+administradora.*bifrost apps restore`)}}})
}

// Recurso efímero: la clave nueva no llega al state (se usa con echo como consumidor).
func TestCredencialEfimeraNoVaAlState(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	ctx := context.Background()
	admin, _ := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: "tienda"}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.CreateAccount(ctx, "tienda", client.CreateAccountReq{Name: "ci", Role: "deploy", Envs: []string{"prod"}}); err != nil {
		t.Fatal(err)
	}
	factories := map[string]func() (tfprotov6.ProviderServer, error){"bifrost": testFactories["bifrost"], "echo": echoprovider.NewProviderServer()}
	tfresource.UnitTest(t, tfresource.TestCase{
		TerraformVersionChecks:   nil,
		ProtoV6ProviderFactories: factories,
		Steps: []tfresource.TestStep{{
			Config: f.provider() + `
ephemeral "bifrost_service_account_credential" "k" {
  app     = "tienda"
  account = "ci"
}
provider "echo" {
  data = ephemeral.bifrost_service_account_credential.k.key_id
}
resource "echo" "id" {}
`,
			Check: tfresource.TestMatchResourceAttr("echo.id", "data", regexp.MustCompile(`^k[23]$`)), // se abre (y rota) en el plan y en el apply
		}},
	})
}
