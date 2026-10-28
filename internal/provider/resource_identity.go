package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

// defaultKeyTTLDays es la vigencia por omisión de las claves que guarda el state (corta a propósito; configurable por recurso).
const defaultKeyTTLDays = 7

func keyJSON(k client.ServiceAccountKey) string {
	b, _ := json.MarshalIndent(k, "", "  ")
	return string(b)
}

func roleValidators() []validator.String {
	return []validator.String{stringvalidator.OneOf("deploy", "readonly", "manager")}
}

var emptySet = setdefault.StaticValue(types.SetValueMust(types.StringType, []attr.Value{}))

// emptyStringMap es el valor por omisión de un mapa de textos opcional.
var emptyStringMap = mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{}))

// ================= cuenta de servicio =================

var (
	_ resource.Resource                = &serviceAccountResource{}
	_ resource.ResourceWithConfigure   = &serviceAccountResource{}
	_ resource.ResourceWithImportState = &serviceAccountResource{}
	_ resource.ResourceWithModifyPlan  = &serviceAccountResource{}
)

func NewServiceAccountResource() resource.Resource { return &serviceAccountResource{} }

type serviceAccountResource struct{ base }

type serviceAccountModel struct {
	ID            types.String `tfsdk:"id"`
	App           types.String `tfsdk:"app"`
	Name          types.String `tfsdk:"name"`
	Role          types.String `tfsdk:"role"`
	Environments  types.Set    `tfsdk:"environments"`
	ExpiresInDays types.Int64  `tfsdk:"expires_in_days"`
	AccountID     types.String `tfsdk:"account_id"`
	KeyID         types.String `tfsdk:"key_id"`
	KeyJSON       types.String `tfsdk:"key_json"`
}

func (r *serviceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (r *serviceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Una cuenta de servicio de una App (identidad de máquina con clave tipo gcloud). **La clave privada inicial se guarda SENSIBLE en el state** (`key_json`): protege el state (backend cifrado, acceso restringido). " +
			"La API no permite cambiar `role` ni `environments`: cambiarlos reemplaza la cuenta, y como una cuenta revocada sigue ocupando su nombre, hay que cambiar también `name`. Destruir revoca la cuenta.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":  schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"name": schema.StringAttribute{Required: true, Description: "Nombre corto (slug, 2 a 30). El id completo es `nombre@app-<app>`.", Validators: slugValidators("de la cuenta", 2, 30), PlanModifiers: rr()},
			"role": schema.StringAttribute{Required: true, Description: "`deploy`, `readonly` o `manager` (control total de ESTA App: red, cuota, cuentas, vínculos, miembros…, y de nada más; sin `environments`). Solo lo da un administrador, un manager de la App o una cuenta de plataforma con todos los permisos.", Validators: roleValidators(), PlanModifiers: rr()},
			"environments": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet,
				Description:   "Entornos a los que llega la cuenta; vacío = todos (presentes y futuros).",
				PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
			"expires_in_days": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(defaultKeyTTLDays),
				Description: "Vigencia de la clave inicial en días (key_ttl). Por omisión 7: corta a propósito, porque la clave queda en el state; es un valor por omisión, no un tope. Solo cuenta al crear: en una cuenta importada (que no tiene clave en el state) cambiarlo no la reemplaza ni revoca su clave.",
				Validators:  []validator.Int64{int64validator.Between(1, 3650)}, PlanModifiers: []planmodifier.Int64{replaceTTLIfCreatedHere()}},
			"account_id": schema.StringAttribute{Computed: true, Description: "Id completo: `nombre@app-<app>`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key_id":     schema.StringAttribute{Computed: true, Description: "Id de la clave inicial.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key_json": schema.StringAttribute{Computed: true, Sensitive: true, Description: "Archivo de clave JSON (el que consume `key_file`/`key_json` del provider o `bifrost auth activate-service-account`). Solo existe si Terraform creó la cuenta.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func accountByName(a client.App, name string) (client.Account, bool) {
	for _, c := range a.Accounts {
		if c.Name == name {
			return c, true
		}
	}
	return client.Account{}, false
}

func (r *serviceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app := plan.App.ValueString()
	out, err := r.c.CreateAccount(ctx, app, client.CreateAccountReq{Name: plan.Name.ValueString(), Role: plan.Role.ValueString(),
		ExpiresInDays: plan.ExpiresInDays.ValueInt64(), Envs: setStrings(ctx, plan.Environments)})
	if err != nil {
		if client.IsConflict(err) {
			err = fmt.Errorf("%w (una cuenta revocada sigue ocupando su nombre: usa otro `name`)", err)
		}
		apiError(&resp.Diagnostics, "No se pudo crear la cuenta de servicio", err)
		return
	}
	plan.ID = types.StringValue(app + "/" + out.Account.Name)
	plan.AccountID = types.StringValue(out.Account.ID)
	plan.KeyID = types.StringValue(out.KeyFile.PrivateKeyID)
	plan.KeyJSON = types.StringValue(keyJSON(out.KeyFile))
	plan.Environments = strSet(out.Account.Envs)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.AddWarning("La clave privada queda en el state", "`key_json` se guarda como sensible en el state de Terraform; la API no la vuelve a mostrar. Protege el state (backend cifrado y con acceso restringido).")
}

func (r *serviceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.GetApp(ctx, st.App.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la App de la cuenta de servicio", err)
		return
	}
	acc, ok := accountByName(a, st.Name.ValueString())
	if !ok || acc.Revoked {
		resp.State.RemoveResource(ctx)
		return
	}
	st.Role, st.AccountID, st.Environments = types.StringValue(acc.Role), types.StringValue(acc.ID), strSet(acc.Envs)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Update solo ocurre con expires_in_days en una cuenta importada (sin clave en el state): es un valor de creación, no hay nada que cambiar en la API.
func (r *serviceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.KeyID, plan.KeyJSON, plan.AccountID, plan.ID = st.KeyID, st.KeyJSON, st.AccountID, st.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RevokeAccount(ctx, st.App.ValueString(), st.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo revocar la cuenta de servicio", err)
	}
}

func (r *serviceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/cuenta", &resp.Diagnostics)
	if p == nil {
		return
	}
	a, err := r.c.GetApp(ctx, p[0])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la cuenta de servicio", err)
		return
	}
	acc, ok := accountByName(a, p[1])
	if !ok || acc.Revoked {
		resp.Diagnostics.AddError("No se encontró la cuenta de servicio", fmt.Sprintf("La App %q no tiene una cuenta activa %q.", p[0], p[1]))
		return
	}
	m := serviceAccountModel{ID: types.StringValue(req.ID), App: types.StringValue(p[0]), Name: types.StringValue(p[1]), Role: types.StringValue(acc.Role), Environments: strSet(acc.Envs),
		// El valor por omisión del esquema: así el plan de una cuenta importada no propone cambios.
		ExpiresInDays: types.Int64Value(defaultKeyTTLDays), AccountID: types.StringValue(acc.ID), KeyID: types.StringNull(), KeyJSON: types.StringNull()}
	if len(acc.Keys) > 0 {
		m.KeyID = types.StringValue(acc.Keys[0].ID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.AddWarning("La clave privada no se puede recuperar", "Al importar, `key_json` queda vacío: la API solo entrega la clave al crearla. Para una clave nueva usa `bifrost_service_account_key`.")
}

// ================= clave de cuenta de servicio =================

var (
	_ resource.Resource               = &serviceAccountKeyResource{}
	_ resource.ResourceWithConfigure  = &serviceAccountKeyResource{}
	_ resource.ResourceWithModifyPlan = &serviceAccountKeyResource{}
)

func NewServiceAccountKeyResource() resource.Resource { return &serviceAccountKeyResource{} }

type serviceAccountKeyResource struct{ base }

type serviceAccountKeyModel struct {
	ID            types.String `tfsdk:"id"`
	App           types.String `tfsdk:"app"`
	Account       types.String `tfsdk:"account"`
	Keepers       types.Map    `tfsdk:"keepers"`
	ExpiresInDays types.Int64  `tfsdk:"expires_in_days"`
	KeyID         types.String `tfsdk:"key_id"`
	KeyJSON       types.String `tfsdk:"key_json"`
	// KeepPrevious añade la llave sin acortar la vida de las anteriores (dos pipelines a la vez); false ROTA. RevokeOnDestroy revoca esta llave al destruir.
	KeepPrevious    types.Bool `tfsdk:"keep_previous"`
	RevokeOnDestroy types.Bool `tfsdk:"revoke_on_destroy"`
}

func (r *serviceAccountKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account_key"
}

func (r *serviceAccountKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Una clave nueva para una cuenta de servicio. Por omisión ROTA: la API acorta la vida de las anteriores (24 h de gracia); con `keep_previous = true` AÑADE una llave sin tocar las demás (hasta 5 vigentes por cuenta). Cambiar `keepers` genera otra. " +
			"**`key_json` es sensible y queda en el state.** Destruir quita el recurso del state y, con `revoke_on_destroy = true`, revoca solo esta llave (la cuenta y las otras siguen); por omisión no revoca nada. Si la clave se revoca o vence fuera de Terraform, el siguiente apply genera otra.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":     schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"account": schema.StringAttribute{Required: true, Description: "Nombre corto de la cuenta (`bifrost_service_account.x.name`).", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"keepers": schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Cualquier cambio fuerza una rotación (p. ej. una marca de fecha).",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()}},
			"expires_in_days": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(defaultKeyTTLDays), Validators: []validator.Int64{int64validator.Between(1, 3650)},
				Description:   "Vigencia de la clave en días (key_ttl). Por omisión 7; es un valor por omisión, no un tope.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
			"keep_previous": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: []planmodifier.Bool{replaceUnlessNewDefault()},
				Description: "`true`: añade la llave sin acortar la vida de las anteriores (dos pipelines a la vez o un cambio escalonado del secreto). `false`: rota (las anteriores vencen en 24 h)."},
			"revoke_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Al destruir (o reemplazar por un cambio de `keepers`), revoca ESTA llave al instante (`account.key.revoke`). Por omisión no revoca. Cuidado al combinarlo con rotar: la llave vieja se revoca sin esperar su gracia."},
			"key_id":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"key_json": schema.StringAttribute{Computed: true, Sensitive: true, Description: "Archivo de clave JSON.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *serviceAccountKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceAccountKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, acc := plan.App.ValueString(), plan.Account.ValueString()
	kf, err := r.c.IssueKey(ctx, app, acc, plan.ExpiresInDays.ValueInt64(), plan.KeepPrevious.ValueBool())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo generar la clave", err)
		return
	}
	plan.KeyID = types.StringValue(kf.PrivateKeyID)
	plan.KeyJSON = types.StringValue(keyJSON(kf))
	plan.ID = types.StringValue(app + "/" + acc + "/" + kf.PrivateKeyID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.AddWarning("La clave privada queda en el state", "`key_json` se guarda como sensible en el state de Terraform; protégelo.")
}

func (r *serviceAccountKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st serviceAccountKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.GetApp(ctx, st.App.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la cuenta", err)
		return
	}
	acc, ok := accountByName(a, st.Account.ValueString())
	if !ok || acc.Revoked {
		resp.State.RemoveResource(ctx)
		return
	}
	for _, k := range acc.Keys {
		if k.ID != st.KeyID.ValueString() {
			continue
		}
		expired := false
		if k.Expires != nil {
			if t, perr := time.Parse(time.RFC3339, *k.Expires); perr == nil && !t.After(time.Now()) {
				expired = true
			}
		}
		if k.Revoked || expired {
			resp.State.RemoveResource(ctx) // el siguiente apply rota
			return
		}
		return
	}
	resp.State.RemoveResource(ctx)
}

// Update solo guarda `revoke_on_destroy` (y el valor por omisión de atributos añadidos en versiones nuevas del provider): la llave en sí no cambia.
func (r *serviceAccountKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st serviceAccountKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.KeyID, plan.KeyJSON = st.ID, st.KeyID, st.KeyJSON
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st serviceAccountKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !st.RevokeOnDestroy.ValueBool() {
		resp.Diagnostics.AddWarning("La clave no se revoca", "Se quita del state, pero la clave sigue válida hasta que venza (las claves anteriores a una rotación vencen solas) o se revoque. Usa `revoke_on_destroy = true` para revocarla al destruir.")
		return
	}
	// 404 (la cuenta o la llave ya no existen) y 409 (ya revocada) son el estado deseado.
	if err := r.c.RevokeKey(ctx, st.App.ValueString(), st.Account.ValueString(), st.KeyID.ValueString()); err != nil && !client.IsNotFound(err) && !client.IsConflict(err) {
		apiError(&resp.Diagnostics, "No se pudo revocar la clave", err)
	}
}

// ================= miembro =================

var (
	_ resource.Resource                = &memberResource{}
	_ resource.ResourceWithConfigure   = &memberResource{}
	_ resource.ResourceWithImportState = &memberResource{}
)

func NewMemberResource() resource.Resource { return &memberResource{} }

type memberResource struct{ base }

type memberModel struct {
	ID    types.String `tfsdk:"id"`
	App   types.String `tfsdk:"app"`
	Email types.String `tfsdk:"email"`
	Role  types.String `tfsdk:"role"`
}

func (r *memberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_member"
}

func (r *memberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Una persona con permiso sobre una App. La App no puede quedarse sin owner (la API devuelve 409 al degradar o quitar al último).",
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":   schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"email": schema.StringAttribute{Required: true, Description: "Correo en minúsculas.", Validators: emailValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"role":  schema.StringAttribute{Required: true, Description: "`owner`, `deploy` o `readonly`.", Validators: []validator.String{stringvalidator.OneOf("owner", "deploy", "readonly")}},
		},
	}
}

func (r *memberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan memberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c.AddMember(ctx, plan.App.ValueString(), plan.Email.ValueString(), plan.Role.ValueString()); err != nil {
		if client.IsConflict(err) {
			err = fmt.Errorf("%w (si ya es miembro, impórtalo con `terraform import`)", err)
		}
		apiError(&resp.Diagnostics, "No se pudo añadir al miembro", err)
		return
	}
	plan.ID = types.StringValue(plan.App.ValueString() + "/" + plan.Email.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *memberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st memberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.GetApp(ctx, st.App.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la App", err)
		return
	}
	for _, m := range a.Members {
		if m.Email == st.Email.ValueString() {
			st.Role = types.StringValue(m.Role)
			resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *memberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan memberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c.SetMemberRole(ctx, plan.App.ValueString(), plan.Email.ValueString(), plan.Role.ValueString()); err != nil {
		apiError(&resp.Diagnostics, "No se pudo cambiar el rol del miembro", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *memberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st memberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RemoveMember(ctx, st.App.ValueString(), st.Email.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo quitar al miembro", err)
	}
}

func (r *memberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/correo", &resp.Diagnostics)
	if p == nil {
		return
	}
	a, err := r.c.GetApp(ctx, p[0])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar al miembro", err)
		return
	}
	for _, m := range a.Members {
		if m.Email == p[1] {
			resp.Diagnostics.Append(resp.State.Set(ctx, &memberModel{ID: types.StringValue(req.ID), App: types.StringValue(p[0]), Email: types.StringValue(p[1]), Role: types.StringValue(m.Role)})...)
			return
		}
	}
	resp.Diagnostics.AddError("No se encontró al miembro", fmt.Sprintf("%q no es miembro de %q.", p[1], p[0]))
}

// ================= vínculo de federación =================

var (
	_ resource.Resource                = &bindingResource{}
	_ resource.ResourceWithConfigure   = &bindingResource{}
	_ resource.ResourceWithImportState = &bindingResource{}
)

func NewBindingResource() resource.Resource { return &bindingResource{} }

type bindingResource struct{ base }

type bindingModel struct {
	ID           types.String `tfsdk:"id"`
	App          types.String `tfsdk:"app"`
	BindingID    types.String `tfsdk:"binding_id"`
	Kind         types.String `tfsdk:"kind"`
	Issuer       types.String `tfsdk:"issuer"`
	ProjectPath  types.String `tfsdk:"project_path"`
	RefProtected types.Bool   `tfsdk:"ref_protected"`
	Claims       types.Map    `tfsdk:"claims"`
	Role         types.String `tfsdk:"role"`
	Environments types.Set    `tfsdk:"environments"`
	Branches     types.Set    `tfsdk:"branches"`
}

// branchRe y branchOK replican la validación del servidor: nombres de rama exactos o con un «*» final, sin «..», «//» ni «/» o «-» en los extremos.
var branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+\*?$`)

func branchOK(b string) bool {
	return b != "*" && len(b) <= 100 && branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "-") && !strings.HasSuffix(b, "/")
}

type branchValidator struct{}

func (branchValidator) Description(context.Context) string {
	return "nombre de rama exacto o con un `*` final (`release/*`); letras, números, `.`, `_`, `-` y `/`"
}
func (v branchValidator) MarkdownDescription(c context.Context) string { return v.Description(c) }
func (branchValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !branchOK(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Rama no válida", fmt.Sprintf("%q no es una rama válida: usa un nombre exacto (`main`) o con un `*` final (`release/*`); para permitir cualquier rama deja la lista vacía.", req.ConfigValue.ValueString()))
	}
}

func (r *bindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_binding"
}

func (r *bindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	emptyMap := mapdefault.StaticValue(types.MapValueMust(types.StringType, map[string]attr.Value{}))
	resp.Schema = schema.Schema{
		Description: "Vínculo de federación del CI: un repo (ID token de GitLab) puede operar la App sin secretos guardados. Se edita en sitio (proyecto, rama protegida, ramas, condiciones, entornos y rol; conserva su id y su último canje); " +
			"cambiar `app`, `kind` o `issuer` lo reemplaza. Un (emisor, project_path) solo puede apuntar a una App en toda la plataforma.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":        schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"binding_id": schema.StringAttribute{Computed: true, Description: "Id que asigna el servidor.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"kind":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("gitlab"), Validators: []validator.String{stringvalidator.OneOf("gitlab")}, PlanModifiers: rr()},
			"issuer":     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("https://gitlab.com"), Description: "Emisor del ID token; debe estar permitido en el cluster.", PlanModifiers: rr()},
			"project_path": schema.StringAttribute{Required: true, Description: "`grupo/repo` o `grupo/*` (todo un grupo).",
				Validators: []validator.String{stringvalidator.LengthBetween(3, 200), stringvalidator.RegexMatches(projectPathRe, "debe ser grupo/repo o grupo/* (sin espacios, sin `..`, comodín solo al final)")}},
			"ref_protected": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Exige que el job corra en una rama/tag protegida."},
			"claims":        schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptyMap, Description: "Condiciones extra sobre claims del ID token (máx 8; valor no vacío), p. ej. `environment = \"production\"`."},
			"role":          schema.StringAttribute{Required: true, Description: "`deploy`, `readonly` o `manager`. Un vínculo `manager` exige `ref_protected = true`, `branches` con nombres exactos (sin `*`), `claims` con `environment`, un `project_path` exacto y `environments` vacío.", Validators: roleValidators()},
			"environments":  schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Description: "Entornos a los que llega; vacío = todos (también los futuros)."},
			"branches": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet,
				Validators:  []validator.Set{setvalidator.SizeAtMost(20), setvalidator.ValueStringsAre(branchValidator{})},
				Description: "Ramas desde las que puede desplegar: nombres exactos o con un `*` final (`release/*`), máximo 20. Con lista, el token debe ser de una **rama** cuyo nombre coincida (una etiqueta con el mismo nombre no pasa); vacío = cualquier ref."},
		},
	}
}

func (r *bindingResource) fill(m *bindingModel, app string, b client.Binding) {
	m.ID = types.StringValue(app + "/" + b.ID)
	m.App, m.BindingID, m.Kind, m.Issuer = types.StringValue(app), types.StringValue(b.ID), types.StringValue(b.Kind), types.StringValue(b.Issuer)
	m.ProjectPath, m.RefProtected, m.Role = types.StringValue(b.ProjectPath), types.BoolValue(b.RefProtected), types.StringValue(b.Role)
	cl := b.Claims
	if cl == nil {
		cl = map[string]string{}
	}
	m.Claims, m.Environments, m.Branches = strMap(cl), strSet(b.Envs), strSet(b.Branches)
}

func (r *bindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan bindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app := plan.App.ValueString()
	b, err := r.c.CreateBinding(ctx, app, client.CreateBindingReq{Kind: plan.Kind.ValueString(), Issuer: plan.Issuer.ValueString(), ProjectPath: plan.ProjectPath.ValueString(),
		RefProtected: plan.RefProtected.ValueBool(), Claims: mapStrings(ctx, plan.Claims), Role: plan.Role.ValueString(), Envs: setStrings(ctx, plan.Environments), Branches: setStrings(ctx, plan.Branches)})
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo crear el vínculo", err)
		return
	}
	r.fill(&plan, app, b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st bindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.GetApp(ctx, st.App.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la App", err)
		return
	}
	for _, b := range a.Bindings {
		if b.ID == st.BindingID.ValueString() {
			r.fill(&st, st.App.ValueString(), b)
			resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *bindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st bindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app := plan.App.ValueString()
	// PUT reemplaza los campos editables; el tipo y el emisor van tal cual están (el servidor rechaza cambiarlos).
	b, err := r.c.UpdateBinding(ctx, app, st.BindingID.ValueString(), client.CreateBindingReq{Kind: st.Kind.ValueString(), Issuer: st.Issuer.ValueString(), ProjectPath: plan.ProjectPath.ValueString(),
		RefProtected: plan.RefProtected.ValueBool(), Claims: mapStrings(ctx, plan.Claims), Role: plan.Role.ValueString(), Envs: setStrings(ctx, plan.Environments), Branches: setStrings(ctx, plan.Branches)})
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo actualizar el vínculo", err)
		return
	}
	r.fill(&plan, app, b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st bindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RemoveBinding(ctx, st.App.ValueString(), st.BindingID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo quitar el vínculo", err)
	}
}

func (r *bindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/id-del-vínculo", &resp.Diagnostics)
	if p == nil {
		return
	}
	a, err := r.c.GetApp(ctx, p[0])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar el vínculo", err)
		return
	}
	for _, b := range a.Bindings {
		if b.ID == p[1] {
			var m bindingModel
			r.fill(&m, p[0], b)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.Diagnostics.AddError("No se encontró el vínculo", fmt.Sprintf("La App %q no tiene un vínculo con id %q (el id sale de `GET /apps/%s/bindings`).", p[0], p[1], p[0]))
}

// ================= cuota por omisión =================

var (
	_ resource.Resource                = &quotaResource{}
	_ resource.ResourceWithConfigure   = &quotaResource{}
	_ resource.ResourceWithImportState = &quotaResource{}
)

func NewQuotaResource() resource.Resource { return &quotaResource{} }

type quotaResource struct{ base }

type quotaResModel struct {
	ID     types.String `tfsdk:"id"`
	App    types.String `tfsdk:"app"`
	CPU    types.String `tfsdk:"cpu"`
	Memory types.String `tfsdk:"memory"`
	Pods   types.Int64  `tfsdk:"pods"`
	PVCs   types.Int64  `tfsdk:"pvcs"`
}

func (r *quotaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_quota"
}

func (r *quotaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := quotaAttrs(true)
	attrs["id"] = schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	attrs["app"] = schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}}
	resp.Schema = schema.Schema{
		Description: "Cuota por omisión de los entornos NUEVOS de una App (solo administradores). **No cambia la cuota de los entornos que ya existen** (la API no lo permite). Destruir el recurso no restablece nada: solo deja de gestionarla.",
		Attributes:  attrs,
	}
}

func (r *quotaResource) apply(ctx context.Context, m *quotaResModel) error {
	a, err := r.c.SetQuota(ctx, m.App.ValueString(), client.Quota{CPU: m.CPU.ValueString(), Memory: m.Memory.ValueString(), Pods: m.Pods.ValueInt64(), PVCs: m.PVCs.ValueInt64()})
	if err != nil {
		return err
	}
	r.fill(m, a)
	return nil
}

func (r *quotaResource) fill(m *quotaResModel, a client.App) {
	m.ID, m.App = types.StringValue(a.Name), types.StringValue(a.Name)
	m.CPU, m.Memory, m.Pods, m.PVCs = types.StringValue(a.Quota.CPU), types.StringValue(a.Quota.Memory), types.Int64Value(a.Quota.Pods), types.Int64Value(a.Quota.PVCs)
}

func (r *quotaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan quotaResModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "No se pudo fijar la cuota", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *quotaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st quotaResModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.GetApp(ctx, st.App.ValueString())
	if client.IsNotFound(err) || (err == nil && a.Status == client.StatusArchived) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la cuota", err)
		return
	}
	r.fill(&st, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *quotaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan quotaResModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "No se pudo fijar la cuota", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *quotaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("La cuota no se restablece", "La API no tiene 'cuota por omisión del sistema': el valor actual se conserva y solo se deja de gestionar.")
}

func (r *quotaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	a, err := r.c.GetApp(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la cuota", err)
		return
	}
	var m quotaResModel
	r.fill(&m, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *serviceAccountResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	warnKeyInState(req, resp)
}

func (r *serviceAccountKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	warnKeyInState(req, resp)
}

// replaceTTLIfCreatedHere reemplaza la cuenta por un cambio de expires_in_days SOLO si Terraform la creó (tiene la clave en el state): en una cuenta
// importada el valor no se puede leer de la API y un reemplazo revocaría la clave del pipeline sin motivo.
func replaceTTLIfCreatedHere() planmodifier.Int64 {
	return int64planmodifier.RequiresReplaceIf(func(ctx context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
		if req.State.Raw.IsNull() || req.StateValue.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.Equal(req.PlanValue) {
			return
		}
		var key types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, rootAttr("key_json"), &key)...)
		resp.RequiresReplace = !key.IsNull() && key.ValueString() != ""
	}, "Cambiar la vigencia de la clave inicial reemplaza la cuenta si Terraform la creó.", "Cambiar la vigencia de la clave inicial reemplaza la cuenta si Terraform la creó.")
}

// replaceUnlessNewDefault reemplaza el recurso si cambia el booleano, salvo que el state anterior no lo conociera (null: el atributo se añadió en una versión
// nueva del provider) y el plan sea el valor por omisión false: actualizar el provider no debe rotar una clave que ya existe.
func replaceUnlessNewDefault() planmodifier.Bool {
	return boolplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
		if req.State.Raw.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.Equal(req.PlanValue) {
			return
		}
		if req.StateValue.IsNull() && !req.PlanValue.ValueBool() {
			return
		}
		resp.RequiresReplace = true
	}, "Cambiar si la llave rota o añade genera una llave nueva.", "Cambiar si la llave rota o añade genera una llave nueva.")
}
