package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

// limpia BIFROST_* para que el entorno de quien corre las pruebas no las contamine.
func cleanEnv(t *testing.T) {
	for _, k := range []string{"BIFROST_API", "BIFROST_TOKEN", "BIFROST_KEY_FILE", "BIFROST_KEY_JSON", "BIFROST_ID_TOKEN", "BIFROST_ID_TOKEN_ENV",
		"BIFROST_ID_TOKEN_GRANT", "BIFROST_FEDERATED_APP", "BIFROST_FEDERATED_PLATFORM", "BIFROST_CA_FILE", "BIFROST_CA_PEM"} {
		t.Setenv(k, "")
	}
}

// Las identidades de máquina solo leen su App, así que todas las pruebas usan la App `tienda`.
const listApps = `
data "bifrost_app" "a" { name = "tienda" }
output "n" { value = length(data.bifrost_app.a.environments) }
`

func TestProviderConfiguracionInvalida(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	cases := map[string]struct{ cfg, err string }{
		"sin autenticación": {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n}\n%s", f.srv.URL, listApps), `Falta la autenticación de Bifröst`},
		"sin api":           {fmt.Sprintf("provider \"bifrost\" {\n token = %q\n}\n%s", fakeapi.DirectAdminTok, listApps), `Falta la dirección de Bifröst`},
		"dos formas":        {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n token = \"x\"\n id_token = \"y\"\n}\n%s", f.srv.URL, listApps), `más de una forma de autenticación`},
		"http no local":     {fmt.Sprintf("provider \"bifrost\" {\n api = \"http://plane.example.com\"\n token = %q\n}\n%s", fakeapi.DirectAdminTok, listApps), `https`},
		"grant inválido":    {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n id_token = \"x\"\n id_token_grant = \"otro\"\n}\n%s", f.srv.URL, listApps), `federated.*user`},
		"clave ilegible":    {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n key_file = \"/no/existe.json\"\n}\n%s", f.srv.URL, listApps), `No se pudo leer la clave`},
		"clave inválida":    {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n key_json = \"{\\\"type\\\":\\\"x\\\"}\"\n}\n%s", f.srv.URL, listApps), `Clave de cuenta de servicio no válida`},
		"CA ilegible":       {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n token = \"x\"\n ca_file = \"/no/existe.pem\"\n}\n%s", f.srv.URL, listApps), `No se pudo leer la CA`},
		"token malo":        {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n token = \"x\"\n}\n%s", f.srv.URL, listApps), `(?s)Token no válido.*credencial no es válida`},
		"id token vacío":    {fmt.Sprintf("provider \"bifrost\" {\n api = %q\n id_token_env = \"BIFROST_VACIA\"\n}\n%s", f.srv.URL, listApps), `BIFROST_VACIA.*vacía`},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			tfresource.UnitTest(t, tfresource.TestCase{ProtoV6ProviderFactories: testFactories,
				Steps: []tfresource.TestStep{{Config: tc.cfg, ExpectError: regexp.MustCompile(tc.err)}}})
		})
	}
}

func TestProviderFormasDeAutenticacion(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	ctx := context.Background()
	admin, _ := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: "tienda"}); err != nil {
		t.Fatal(err)
	}
	acc, err := admin.CreateAccount(ctx, "tienda", client.CreateAccountReq{Name: "ci", Role: "readonly"})
	if err != nil {
		t.Fatal(err)
	}
	kj, _ := json.Marshal(acc.KeyFile)
	keyPath := filepath.Join(t.TempDir(), "clave.json")
	if err := os.WriteFile(keyPath, kj, 0o600); err != nil {
		t.Fatal(err)
	}
	one := tfresource.TestCheckOutput("n", "1")
	prov := func(body string) string {
		return fmt.Sprintf("provider \"bifrost\" {\n api = %q\n%s}\n%s", f.srv.URL, body, listApps)
	}
	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: testFactories,
		Steps: []tfresource.TestStep{
			{Config: prov(fmt.Sprintf(" token = %q\n", fakeapi.DirectAdminTok)), Check: one},
			{Config: prov(fmt.Sprintf(" key_file = %q\n", keyPath)), Check: one},
			{Config: prov(fmt.Sprintf(" key_json = %q\n", string(kj))), Check: one},
			// La clave lleva su `api`: se puede omitir en el provider.
			{Config: fmt.Sprintf("provider \"bifrost\" {\n key_file = %q\n}\n%s", keyPath, listApps), Check: one},
			{PreConfig: func() { t.Setenv("CI_JOB_JWT_V2", fakeapi.CIIDToken) }, Config: prov(" id_token_env = \"CI_JOB_JWT_V2\"\n federated_app = \"" + fakeapi.CIApp + "\"\n"), Check: one},
			{PreConfig: func() { t.Setenv("MI_ID_TOKEN", fakeapi.AdminIDToken) }, Config: prov(" id_token_env = \"MI_ID_TOKEN\"\n id_token_grant = \"user\"\n"), Check: one},
			{Config: prov(fmt.Sprintf(" id_token = %q\n id_token_grant = \"user\"\n", fakeapi.AdminIDToken)), Check: one},
			// Solo variables de entorno.
			{PreConfig: func() {
				t.Setenv("BIFROST_API", f.srv.URL)
				t.Setenv("BIFROST_TOKEN", fakeapi.DirectAdminTok)
			}, Config: "provider \"bifrost\" {}\n" + listApps, Check: one},
		},
	})
}
