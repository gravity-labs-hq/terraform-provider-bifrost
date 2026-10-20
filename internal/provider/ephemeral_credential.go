package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

var (
	_ ephemeral.EphemeralResource              = &credentialEphemeral{}
	_ ephemeral.EphemeralResourceWithConfigure = &credentialEphemeral{}
)

func NewCredentialEphemeral() ephemeral.EphemeralResource { return &credentialEphemeral{} }

type credentialEphemeral struct{ c *client.Client }

type credentialModel struct {
	App           types.String `tfsdk:"app"`
	Account       types.String `tfsdk:"account"`
	ExpiresInDays types.Int64  `tfsdk:"expires_in_days"`
	KeyID         types.String `tfsdk:"key_id"`
	KeyJSON       types.String `tfsdk:"key_json"`
}

func (e *credentialEphemeral) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account_credential"
}

func (e *credentialEphemeral) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Clave nueva de una cuenta de servicio que **nunca se guarda en el state ni en el plan** (recurso efímero, Terraform 1.10 o superior). " +
			"Cada vez que Terraform lo abre (plan o apply) **rota la clave**: la anterior sigue valiendo solo durante el periodo de gracia del servidor. " +
			"Pásala a un atributo de solo escritura (write-only) de otro recurso, p. ej. `value_wo` de una variable de CI, o a un provider. Úsala con vigencia corta.",
		Attributes: map[string]schema.Attribute{
			"app":             schema.StringAttribute{Required: true, Description: "Nombre de la App."},
			"account":         schema.StringAttribute{Required: true, Description: "Nombre corto de la cuenta de servicio."},
			"expires_in_days": schema.Int64Attribute{Optional: true, Description: "Vigencia de la clave en días. Por omisión 1 (valor por omisión, no un tope)."},
			"key_id":          schema.StringAttribute{Computed: true},
			"key_json":        schema.StringAttribute{Computed: true, Sensitive: true, Description: "Archivo de clave JSON."},
		},
	}
}

func (e *credentialEphemeral) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Configuración del provider inesperada", "se esperaba *client.Client")
		return
	}
	e.c = c
}

func (e *credentialEphemeral) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var m credentialModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	days := int64(1)
	if !m.ExpiresInDays.IsNull() && !m.ExpiresInDays.IsUnknown() {
		days = m.ExpiresInDays.ValueInt64()
	}
	kf, err := e.c.RotateKey(ctx, m.App.ValueString(), m.Account.ValueString(), days)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo generar la clave efímera", err)
		return
	}
	m.KeyID, m.KeyJSON = types.StringValue(kf.PrivateKeyID), types.StringValue(keyJSON(kf))
	resp.Diagnostics.Append(resp.Result.Set(ctx, &m)...)
}
