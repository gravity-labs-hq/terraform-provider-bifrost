package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

// Con el log del provider en TRACE, ningún secreto (token, clave privada, contraseña) debe aparecer, pero sí método y ruta de cada llamada.
func TestSecretosFueraDeLosLogs(t *testing.T) {
	cleanEnv(t)
	f := newFakeEnv(t)
	ctx := context.Background()

	admin, _ := client.New(client.Config{API: f.srv.URL, Auth: client.StaticToken(fakeapi.DirectAdminTok)})
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: "tienda"}); err != nil {
		t.Fatal(err)
	}
	acc, err := admin.CreateAccount(ctx, "tienda", client.CreateAccountReq{Name: "ci", Role: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	kj, _ := json.Marshal(acc.KeyFile)

	var buf bytes.Buffer
	old := debugLog
	debugLog = func(_ context.Context, msg string) { buf.WriteString(msg + "\n") }
	defer func() { debugLog = old }()

	configure := func(attrs map[string]string) (*client.Client, *bytes.Buffer) {
		buf.Reset()
		lctx := ctx
		p := New("test")()
		var sr provider.SchemaResponse
		p.Schema(lctx, provider.SchemaRequest{}, &sr)
		ot := sr.Schema.Type().TerraformType(lctx).(tftypes.Object)
		vals := map[string]tftypes.Value{}
		for name, typ := range ot.AttributeTypes {
			if v, ok := attrs[name]; ok {
				vals[name] = tftypes.NewValue(typ, v)
			} else {
				vals[name] = tftypes.NewValue(typ, nil)
			}
		}
		var resp provider.ConfigureResponse
		p.Configure(lctx, provider.ConfigureRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(ot, vals)}}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatal(resp.Diagnostics)
		}
		return resp.ResourceData.(*client.Client), &buf
	}

	for name, attrs := range map[string]map[string]string{
		"token":   {"api": f.srv.URL, "token": fakeapi.DirectAdminTok},
		"clave":   {"api": f.srv.URL, "key_json": string(kj)},
		"idtoken": {"api": f.srv.URL, "id_token": fakeapi.AdminIDToken, "id_token_grant": "user"},
	} {
		c, buf := configure(attrs)
		for i := 0; i < 2; i++ {
			_, _ = c.GetApp(ctx, "tienda")
		}
		_, _ = c.AddMember(ctx, "tienda", "ana@example.com", "deploy")
		log := buf.String()
		if !strings.Contains(log, "bifrost api: GET /v1/apps/tienda -> 200") {
			t.Fatalf("%s: el provider debe registrar método, ruta y código; log:\n%.1500s", name, log)
		}
		for what, s := range map[string]string{"clave privada": "PRIVATE KEY", "token directo": fakeapi.DirectAdminTok, "JSON de clave": string(kj),
			"id token": fakeapi.AdminIDToken, "id de clave": acc.KeyFile.PrivateKeyID + "\"", "cabecera": "Authorization", "Bearer": "Bearer"} {
			if strings.Contains(log, s) {
				t.Errorf("%s: el log contiene %s", name, what)
			}
		}
	}
}
