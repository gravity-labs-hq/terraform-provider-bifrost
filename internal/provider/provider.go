// Package provider implementa el provider de Terraform para Bifröst (terraform-plugin-framework, protocolo v6).
package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

// debugLog escribe una línea de diagnóstico en el log del provider (TF_LOG_PROVIDER=DEBUG). Es una variable para poder comprobar en pruebas
// que ningún secreto llega al log.
var debugLog = func(ctx context.Context, msg string) { tflog.Debug(ctx, msg) }

// Provider es el provider `bifrost`.
type Provider struct{ version string }

// New devuelve el constructor del provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &Provider{version: version} }
}

type providerModel struct {
	API               types.String `tfsdk:"api"`
	Token             types.String `tfsdk:"token"`
	KeyFile           types.String `tfsdk:"key_file"`
	KeyJSON           types.String `tfsdk:"key_json"`
	IDToken           types.String `tfsdk:"id_token"`
	IDTokenEnv        types.String `tfsdk:"id_token_env"`
	IDTokenGrant      types.String `tfsdk:"id_token_grant"`
	FederatedApp      types.String `tfsdk:"federated_app"`
	FederatedPlatform types.String `tfsdk:"federated_platform"`
	CAFile            types.String `tfsdk:"ca_file"`
	CAPEM             types.String `tfsdk:"ca_pem"`
}

func (p *Provider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "bifrost"
	resp.Version = p.version
}

func (p *Provider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Administra Bifröst como código: Apps, entornos, herramientas, cuentas de servicio, miembros, vínculos de CI, red, colas, runners y más. " +
			"Se autentica contra plane (https://plane.<dominio>) con UNA de estas formas: `token`, `key_file`/`key_json` (cuenta de servicio), o `id_token`/`id_token_env` (federación del CI o persona).",
		Attributes: map[string]schema.Attribute{
			"api":                schema.StringAttribute{Optional: true, Description: "Base de plane, p. ej. `https://plane.example.com`. También `BIFROST_API`. Si se omite y hay clave de cuenta de servicio, se usa la `api` de la clave. Solo `https` (http únicamente para localhost)."},
			"token":              schema.StringAttribute{Optional: true, Sensitive: true, Description: "Bearer de Bifröst ya emitido (dura ~1 hora y no se renueva). También `BIFROST_TOKEN`."},
			"key_file":           schema.StringAttribute{Optional: true, Description: "Ruta del archivo JSON de clave de una cuenta de servicio. También `BIFROST_KEY_FILE`."},
			"key_json":           schema.StringAttribute{Optional: true, Sensitive: true, Description: "Contenido JSON de la clave de una cuenta de servicio (p. ej. desde una variable de CI). También `BIFROST_KEY_JSON`."},
			"id_token":           schema.StringAttribute{Optional: true, Sensitive: true, Description: "ID token (JWT) del CI (federación) o de la persona. También `BIFROST_ID_TOKEN`. Es válido ~minutos: pásalo por variable de entorno y no lo guardes."},
			"id_token_env":       schema.StringAttribute{Optional: true, Description: "Nombre de la variable de entorno que contiene el ID token (se lee en cada canje, como hace la CLI `bifrost auth federate --id-token-env`). También `BIFROST_ID_TOKEN_ENV`."},
			"id_token_grant":     schema.StringAttribute{Optional: true, Description: "Cómo se canjea el ID token: `federated` (por omisión; vínculo del CI, rol limitado a una App) o `user` (persona del proveedor de identidad; si es administradora, administra toda la plataforma). También `BIFROST_ID_TOKEN_GRANT`."},
			"federated_app":      schema.StringAttribute{Optional: true, Description: "App destino al canjear un ID token federado (opcional). También `BIFROST_FEDERATED_APP`."},
			"federated_platform": schema.StringAttribute{Optional: true, Description: "Nombre de la cuenta de servicio de PLATAFORMA que asume el job de CI con su ID token (vínculo de la cuenta: proyecto exacto, rama protegida y environment). Con ella Terraform administra Apps, red, colas y runners SIN ser administrador, limitado a los scopes de la cuenta. También `BIFROST_FEDERATED_PLATFORM`."},
			"ca_file":            schema.StringAttribute{Optional: true, Description: "Archivo PEM con una CA adicional para verificar el TLS de plane. También `BIFROST_CA_FILE`. La verificación TLS nunca se desactiva."},
			"ca_pem":             schema.StringAttribute{Optional: true, Description: "CA adicional en PEM (alternativa a `ca_file`). También `BIFROST_CA_PEM`."},
		},
	}
}

func envOr(v types.String, name string) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	return strings.TrimSpace(os.Getenv(name))
}

func (p *Provider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, v := range map[string]types.String{"api": m.API, "token": m.Token, "key_file": m.KeyFile, "key_json": m.KeyJSON, "id_token": m.IDToken, "id_token_env": m.IDTokenEnv,
		"id_token_grant": m.IDTokenGrant, "federated_app": m.FederatedApp, "federated_platform": m.FederatedPlatform, "ca_file": m.CAFile, "ca_pem": m.CAPEM} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(rootAttr(name), "Valor desconocido en la configuración del provider",
				fmt.Sprintf("`%s` aún no se conoce al configurar el provider: no puede depender de otros recursos. Usa una variable o una variable de entorno.", name))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	token := envOr(m.Token, "BIFROST_TOKEN")
	keyFile := envOr(m.KeyFile, "BIFROST_KEY_FILE")
	keyJSON := envOr(m.KeyJSON, "BIFROST_KEY_JSON")
	idToken := envOr(m.IDToken, "BIFROST_ID_TOKEN")
	idEnv := envOr(m.IDTokenEnv, "BIFROST_ID_TOKEN_ENV")
	api := envOr(m.API, "BIFROST_API")
	grant := envOr(m.IDTokenGrant, "BIFROST_ID_TOKEN_GRANT")
	fedApp := envOr(m.FederatedApp, "BIFROST_FEDERATED_APP")
	fedPlat := envOr(m.FederatedPlatform, "BIFROST_FEDERATED_PLATFORM")

	var methods []string
	if token != "" {
		methods = append(methods, "token")
	}
	if keyFile != "" {
		methods = append(methods, "key_file")
	}
	if keyJSON != "" {
		methods = append(methods, "key_json")
	}
	if idToken != "" || idEnv != "" {
		methods = append(methods, "id_token")
	}
	switch {
	case len(methods) == 0:
		resp.Diagnostics.AddError("Falta la autenticación de Bifröst",
			"Indica una forma de autenticarse: `token` (BIFROST_TOKEN), `key_file` (BIFROST_KEY_FILE) o `key_json` (BIFROST_KEY_JSON) de una cuenta de servicio, o `id_token`/`id_token_env` (BIFROST_ID_TOKEN) para federación del CI o persona.")
		return
	case len(methods) > 1:
		resp.Diagnostics.AddError("Hay más de una forma de autenticación", "Se encontraron varias a la vez ("+strings.Join(methods, ", ")+"). Deja solo una (revisa también las variables de entorno BIFROST_*).")
		return
	}

	cfg := client.Config{UserAgent: "terraform-provider-bifrost/" + p.version}
	switch methods[0] {
	case "token":
		cfg.Auth = client.StaticToken(token)
	case "key_file", "key_json":
		raw := []byte(keyJSON)
		if keyFile != "" {
			b, err := os.ReadFile(keyFile)
			if err != nil {
				resp.Diagnostics.AddError("No se pudo leer la clave de la cuenta de servicio", fmt.Sprintf("key_file %q: %v", keyFile, err))
				return
			}
			raw = b
		}
		k, err := client.ParseKeyJSON(raw)
		if err != nil {
			resp.Diagnostics.AddError("Clave de cuenta de servicio no válida", err.Error())
			return
		}
		if api == "" {
			api = k.API
		}
		cfg.Auth = &client.KeyAuth{Key: k}
	case "id_token":
		if grant == "" {
			grant = client.GrantFederated
		}
		if fedPlat != "" && grant == client.GrantUser {
			resp.Diagnostics.AddError("federated_platform exige el canje federado", "federated_platform solo se usa con id_token_grant = federated (por omisión).")
			return
		}
		if fedPlat != "" && fedApp != "" {
			resp.Diagnostics.AddError("federated_app y federated_platform son excluyentes", "Elige la App o la cuenta de plataforma que asume el job de CI.")
			return
		}
		if grant != client.GrantFederated && grant != client.GrantUser {
			resp.Diagnostics.AddAttributeError(rootAttr("id_token_grant"), "Valor no válido", "id_token_grant debe ser `federated` o `user`.")
			return
		}
		read := func() (string, error) {
			if idEnv != "" {
				v := strings.TrimSpace(os.Getenv(idEnv))
				if v == "" {
					return "", fmt.Errorf("la variable de entorno %s (id_token_env) está vacía o no existe", idEnv)
				}
				return v, nil
			}
			return idToken, nil
		}
		cfg.Auth = &client.IDTokenAuth{Grant: grant, App: fedApp, Platform: fedPlat, Read: read}
	}
	if api == "" {
		resp.Diagnostics.AddError("Falta la dirección de Bifröst", "Indica `api` o BIFROST_API (p. ej. https://plane.example.com).")
		return
	}
	cfg.API = api
	if f := envOr(m.CAFile, "BIFROST_CA_FILE"); f != "" {
		b, err := client.LoadCA(f)
		if err != nil {
			resp.Diagnostics.AddError("No se pudo leer la CA", err.Error())
			return
		}
		cfg.CAPEM = b
	} else if pem := envOr(m.CAPEM, "BIFROST_CA_PEM"); pem != "" {
		cfg.CAPEM = []byte(pem)
	}
	cfg.Logf = func(format string, args ...any) { debugLog(ctx, "bifrost api: "+fmt.Sprintf(format, args...)) }

	c, err := client.New(cfg)
	if err != nil {
		resp.Diagnostics.AddError("Configuración de Bifröst no válida", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
	resp.EphemeralResourceData = c
}

func (p *Provider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewAppResource, NewEnvironmentResource, NewServiceResource, NewServiceAccountResource, NewServiceAccountKeyResource,
		NewMemberResource, NewBindingResource, NewPlatformAccountResource, NewQuotaResource, NewNetworkResource, NewHostRulesResource, NewAccessResource,
		NewScalingResource, NewScheduleResource, NewQueueResource, NewReplicationLinkResource, NewRunnerPoolResource, NewAdminResource,
	}
}

func (p *Provider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewAppDataSource, NewAppsDataSource, NewEnvironmentDataSource, NewServiceDataSource, NewPersonDataSource}
}

// EphemeralResources implementa provider.ProviderWithEphemeralResources.
func (p *Provider) EphemeralResources(context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{NewCredentialEphemeral}
}
