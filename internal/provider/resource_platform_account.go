package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

var (
	_ resource.Resource                = &platformAccountResource{}
	_ resource.ResourceWithConfigure   = &platformAccountResource{}
	_ resource.ResourceWithImportState = &platformAccountResource{}
	_ resource.ResourceWithModifyPlan  = &platformAccountResource{}
)

func NewPlatformAccountResource() resource.Resource { return &platformAccountResource{} }

type platformAccountResource struct{ base }

type ciBindingModel struct {
	ProjectPath types.String `tfsdk:"project_path"`
	Environment types.String `tfsdk:"environment"`
	Issuer      types.String `tfsdk:"issuer"`
}

type platformAccountModel struct {
	ID              types.String    `tfsdk:"id"`
	Name            types.String    `tfsdk:"name"`
	Scopes          types.Set       `tfsdk:"scopes"`
	EffectiveScopes types.Set       `tfsdk:"effective_scopes"`
	Apps            types.Set       `tfsdk:"apps"`
	AppPrefix       types.String    `tfsdk:"app_prefix"`
	ExpiresInDays   types.Int64     `tfsdk:"expires_in_days"`
	Keepers         types.Map       `tfsdk:"keepers"`
	AllowedDomains  types.Set       `tfsdk:"allowed_domains"`
	AllowIdentities types.Bool      `tfsdk:"allow_identities"`
	CIBinding       *ciBindingModel `tfsdk:"ci_binding"`
	AccountID       types.String    `tfsdk:"account_id"`
	ExpiresAt       types.String    `tfsdk:"expires_at"`
	KeyID           types.String    `tfsdk:"key_id"`
	KeyExpiresAt    types.String    `tfsdk:"key_expires_at"`
	KeyJSON         types.String    `tfsdk:"key_json"`
}

var scopeNames = []string{"apps:read", "apps:create", "apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage", "queues:read", "queues:manage",
	"runners:read", "runners:manage", "replication:read", "replication:propose", "schedule:manage"}

func (r *platformAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform_account"
}

func (r *platformAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Cuenta de servicio de PLATAFORMA: la identidad con la que Terraform corre desde un CI **sin ser administrador**. Tiene solo los `scopes` indicados (nunca sobre administradores, personas ni otras cuentas de plataforma) " +
			"y solo toca las Apps que ella crea, las de `apps` o las que empiezan con `app_prefix`. **Solo una persona administradora puede crear este recurso** (es el paso de arranque). " +
			"La clave privada queda SENSIBLE en el state. La caducidad es obligatoria; cambiar `expires_in_days` o `keepers` ROTA la clave y renueva la caducidad. El nombre de una cuenta revocada no se reutiliza.",
		Attributes: map[string]schema.Attribute{
			"id": idAttr(),
			"name": schema.StringAttribute{Required: true, Description: "Nombre (2 a 30, minúsculas ASCII, números y guiones; no empieza con «admin»).", PlanModifiers: rr(),
				Validators: []validator.String{stringvalidator.RegexMatches(slugRe, "debe ser minúsculas ASCII, números y guiones medios"), stringvalidator.LengthBetween(2, 30)}},
			"scopes": schema.SetAttribute{Required: true, ElementType: types.StringType, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Description: "Scopes (lista cerrada): " + joinScopes() + ". Los de escritura incluyen su lectura. `apps:create`, `queues:manage`, `runners:manage` y `replication:propose` exigen `app_prefix`; `identities:manage` exige además `allow_identities = true` y `apps` o `app_prefix`. `schedule:manage` abre SOLO la programación de carga (`bifrost_schedule`) de las Apps permitidas: no incluye `apps:manage` ni lo incluye ningún otro scope.",
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.OneOf(scopeNames...))}},
			"effective_scopes": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "Scopes vigentes en el servidor: los de `scopes` más las lecturas que incluyen."},
			"apps": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Description: "Apps asignadas explícitamente además de las que cree."},
			"allowed_domains": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()},
				Description: "Dominios de correo (p. ej. `example.org`) a los que la cuenta puede dar rol de owner o miembro. **Obligatorio con `apps:create` o `members:manage`**; nunca puede nombrar owner a otra cuenta de plataforma."},
			"allow_identities": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
				Description: "Opt-in explícito de `identities:manage` (cuentas de servicio y vínculos de CI de las Apps permitidas = acceso de despliegue). Exige `apps` o `app_prefix`."},
			"app_prefix": schema.StringAttribute{Optional: true, Description: "Prefijo de nombres delegado (Apps, colas, pools, enlaces), p. ej. `tienda-`. Comparación literal.", PlanModifiers: rr(),
				Validators: []validator.String{stringvalidator.LengthBetween(1, 28), stringvalidator.RegexMatches(prefixRe, "minúsculas ASCII, números y guiones")}},
			"expires_in_days": schema.Int64Attribute{Required: true, Description: "Caducidad de la cuenta en días (1 a 365). Cambiarlo rota la clave y renueva la caducidad.", Validators: []validator.Int64{int64validator.Between(1, 365)}},
			"keepers": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
				Description: "Cualquier cambio ROTA la clave (p. ej. una fecha), también el primero tras importar: una cuenta importada nace con un valor centinela `imported = \"true\"`, así que declarar `keepers` por primera vez siempre es un valor nuevo y rota. Si no lo declaras, no cambia nada."},
			"ci_binding": schema.SingleNestedAttribute{Optional: true, PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Description: "Permite que un job de GitLab asuma la cuenta con su ID token, sin secretos guardados. Se exige proyecto exacto, rama protegida y environment.",
				Attributes: map[string]schema.Attribute{
					"project_path": schema.StringAttribute{Required: true, Description: "`grupo/repo` exacto (sin comodines)."},
					"environment":  schema.StringAttribute{Required: true, Description: "Valor exigido de `environment:` en el job (p. ej. `production`)."},
					"issuer":       schema.StringAttribute{Optional: true, Description: "Emisor del ID token (por omisión https://gitlab.com); debe estar permitido en el cluster."},
				}},
			"account_id":     schema.StringAttribute{Computed: true, Description: "`nombre@platform`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"expires_at":     schema.StringAttribute{Computed: true, Description: "Caducidad vigente (RFC 3339)."},
			"key_id":         schema.StringAttribute{Computed: true},
			"key_expires_at": schema.StringAttribute{Computed: true, Description: "Vencimiento de la clave vigente más tardía (RFC 3339; `never` si no vence; vacío si no hay ninguna clave vigente). Si ya venció, está por vencer (menos de 72 h) o no hay clave vigente, el siguiente apply ROTA la clave."},
			"key_json": schema.StringAttribute{Computed: true, Sensitive: true,
				Description: "Archivo de clave JSON para `key_file`/`key_json` del provider en el CI. Sensible; protege el state."},
		},
	}
}

func joinScopes() string {
	out := ""
	for i, s := range scopeNames {
		if i > 0 {
			out += ", "
		}
		out += "`" + s + "`"
	}
	return out
}

func (r *platformAccountResource) fill(m *platformAccountModel, a client.PlatformAccount) {
	m.ID, m.AccountID, m.ExpiresAt = types.StringValue(a.Name), types.StringValue(a.ID), types.StringValue(a.ExpiresAt)
	m.EffectiveScopes, m.Apps, m.AllowedDomains, m.AllowIdentities = strSet(a.Scopes), strSet(a.Apps), strSet(a.AllowedDomains), types.BoolValue(a.AllowIdentities)
	// `scopes` conserva lo que el usuario escribió mientras el servidor tenga lo mismo (más las lecturas implícitas); si no, es deriva.
	if m.Scopes.IsNull() || m.Scopes.IsUnknown() || !sameSet(expandScopes(setStrings(context.Background(), m.Scopes)), a.Scopes) {
		m.Scopes = strSet(minimalScopes(a.Scopes))
	}
	if a.AppPrefix != "" {
		m.AppPrefix = types.StringValue(a.AppPrefix)
	}
	m.KeyExpiresAt = types.StringValue(latestKeyExpiry(a))
}

// latestKeyExpiry devuelve el vencimiento de la clave vigente que más dura: RFC 3339, «never» si alguna no vence, "" si ninguna está vigente.
func latestKeyExpiry(a client.PlatformAccount) string {
	var best time.Time
	have := false
	for _, k := range a.Keys {
		if k.Revoked {
			continue
		}
		if k.Expires == nil || *k.Expires == "" {
			return "never"
		}
		t, err := time.Parse(time.RFC3339, *k.Expires)
		if err != nil {
			continue // una clave con vigencia ilegible no cuenta como vigente (fail-closed)
		}
		if !have || t.After(best) {
			best, have = t, true
		}
	}
	if !have {
		return ""
	}
	return best.UTC().Format(time.RFC3339)
}

// renewWindow: con menos de esto de vida la clave se rota en el siguiente apply.
const renewWindow = 72 * time.Hour

// keyNeedsRotation decide si la clave del state sirve: FAIL-CLOSED hacia rotar. Desconocido, vacío, ilegible, vencido o por vencer = rotar.
func keyNeedsRotation(keyExpires types.String, accountExpires types.String, now time.Time) (bool, string) {
	if keyExpires.IsNull() || keyExpires.IsUnknown() || keyExpires.ValueString() == "" {
		return true, "no hay una clave vigente (o no se pudo comprobar)"
	}
	if keyExpires.ValueString() != "never" {
		t, err := time.Parse(time.RFC3339, keyExpires.ValueString())
		if err != nil || !t.After(now.Add(renewWindow)) {
			return true, "la clave venció o vence en menos de 72 horas"
		}
	}
	at, err := time.Parse(time.RFC3339, accountExpires.ValueString())
	if err != nil || !at.After(now.Add(renewWindow)) {
		return true, "la cuenta venció o vence en menos de 72 horas"
	}
	return false, ""
}

// importedKeepers es el valor centinela de `keepers` en una cuenta importada.
func importedKeepers() types.Map {
	return types.MapValueMust(types.StringType, map[string]attr.Value{"imported": types.StringValue("true")})
}

// effectiveDays estima los días que le quedan a la cuenta (lo único que la API devuelve de expires_in_days); error si no se puede leer.
func effectiveDays(accountExpires string, now time.Time) (int64, error) {
	t, err := time.Parse(time.RFC3339, accountExpires)
	if err != nil {
		return 0, err
	}
	d := t.Sub(now)
	if d < 0 {
		d = 0
	}
	return int64((d + 24*time.Hour - 1) / (24 * time.Hour)), nil
}

// shouldRotate decide si un apply rota la clave. Siempre rota ante: cambio de keepers (incluido el primero tras importar), cambio de expires_in_days,
// clave vencida/por vencer/inexistente o cualquier dato ilegible. Solo NO rota cuando declarar expires_in_days por primera vez tras importar coincide con los
// días efectivos que le quedan a la cuenta (±1) y nada más cambió.
func shouldRotate(st, plan platformAccountModel, now time.Time) (bool, string) {
	if plan.Keepers.IsUnknown() || !plan.Keepers.Equal(st.Keepers) {
		return true, "cambió keepers"
	}
	if need, why := keyNeedsRotation(st.KeyExpiresAt, st.ExpiresAt, now); need {
		return true, why
	}
	if plan.ExpiresInDays.IsUnknown() || plan.ExpiresInDays.IsNull() {
		return true, "expires_in_days no se pudo leer"
	}
	if st.ExpiresInDays.IsNull() || st.ExpiresInDays.IsUnknown() { // cuenta importada: se compara con lo efectivo
		eff, err := effectiveDays(st.ExpiresAt.ValueString(), now)
		if err != nil {
			return true, "no se pudo leer la caducidad de la cuenta"
		}
		d := plan.ExpiresInDays.ValueInt64() - eff
		if d < -1 || d > 1 {
			return true, "expires_in_days difiere de la caducidad efectiva de la cuenta importada"
		}
		return false, ""
	}
	if !st.ExpiresInDays.Equal(plan.ExpiresInDays) {
		return true, "cambió expires_in_days"
	}
	return false, ""
}

func (r *platformAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan platformAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	cr := client.CreatePlatformAccountReq{Name: plan.Name.ValueString(), Scopes: setStrings(ctx, plan.Scopes), Apps: setStrings(ctx, plan.Apps), AppPrefix: str(plan.AppPrefix),
		ExpiresInDays: plan.ExpiresInDays.ValueInt64(), AllowedDomains: setStrings(ctx, plan.AllowedDomains), AllowIdentities: plan.AllowIdentities.ValueBool()}
	if b := plan.CIBinding; b != nil {
		cr.Bindings = []client.PlatformBindingSpec{{Issuer: str(b.Issuer), ProjectPath: b.ProjectPath.ValueString(), Environment: b.Environment.ValueString()}}
	}
	out, err := r.c.CreatePlatformAccount(ctx, cr)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo crear la cuenta de plataforma", err)
		return
	}
	if plan.Keepers.IsUnknown() || plan.Keepers.IsNull() {
		plan.Keepers = types.MapValueMust(types.StringType, map[string]attr.Value{})
	}
	r.fill(&plan, out.Account)
	plan.KeyID, plan.KeyJSON = types.StringValue(out.KeyFile.PrivateKeyID), types.StringValue(keyJSON(out.KeyFile))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.AddWarning("La clave privada queda en el state", "`key_json` se guarda como sensible en el state de Terraform; la API no la vuelve a mostrar. Protege el state y entrégala al CI como variable enmascarada o de tipo archivo.")
}

func (r *platformAccountResource) find(ctx context.Context, name string) (client.PlatformAccount, bool, error) {
	l, err := r.c.ListPlatformAccounts(ctx)
	if err != nil {
		return client.PlatformAccount{}, false, err
	}
	for _, a := range l {
		if a.Name == name {
			return a, true, nil
		}
	}
	return client.PlatformAccount{}, false, nil
}

func (r *platformAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st platformAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, found, err := r.find(ctx, st.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la cuenta de plataforma", err)
		return
	}
	if !found || a.Revoked {
		resp.State.RemoveResource(ctx)
		return
	}
	r.fill(&st, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *platformAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st platformAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rotate, why := shouldRotate(st, plan, time.Now())
	if !rotate {
		// Solo estado: se adopta el valor declarado de expires_in_days (coincide con la caducidad efectiva); la clave NO cambia ni se afirma que se rotó.
		plan.ID, plan.AccountID, plan.ExpiresAt, plan.KeyID, plan.KeyJSON, plan.EffectiveScopes, plan.KeyExpiresAt = st.ID, st.AccountID, st.ExpiresAt, st.KeyID, st.KeyJSON, st.EffectiveScopes, st.KeyExpiresAt
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	_ = why
	out, err := r.c.RotatePlatformAccount(ctx, plan.Name.ValueString(), plan.ExpiresInDays.ValueInt64())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo rotar la clave de la cuenta de plataforma", err)
		return
	}
	r.fill(&plan, out.Account)
	plan.KeyID, plan.KeyJSON = types.StringValue(out.KeyFile.PrivateKeyID), types.StringValue(keyJSON(out.KeyFile))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.AddWarning("La clave nueva queda en el state ("+why+")", "La clave anterior sigue valiendo solo durante el periodo de gracia del servidor (1 hora por omisión) y luego deja de valer: actualiza el CI con la clave nueva.")
}

func (r *platformAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st platformAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RevokePlatformAccount(ctx, st.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo revocar la cuenta de plataforma", err)
	}
}

func (r *platformAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	a, found, err := r.find(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la cuenta de plataforma", err)
		return
	}
	if !found || a.Revoked {
		resp.Diagnostics.AddError("No se encontró la cuenta de plataforma", fmt.Sprintf("No hay una cuenta activa %q.", req.ID))
		return
	}
	m := platformAccountModel{Name: types.StringValue(a.Name), AllowedDomains: types.SetNull(types.StringType), AllowIdentities: types.BoolNull(), AppPrefix: types.StringNull(), ExpiresInDays: types.Int64Null(), Keepers: importedKeepers(),
		KeyID: types.StringNull(), KeyJSON: types.StringNull()}
	r.fill(&m, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.AddWarning("La clave privada no se puede recuperar", "Al importar `key_json` queda vacío: declarar `keepers` (cualquier valor) o un `expires_in_days` distinto de lo que le queda a la cuenta genera una clave nueva.")
	if need, why := keyNeedsRotation(m.KeyExpiresAt, m.ExpiresAt, time.Now()); need {
		resp.Diagnostics.AddWarning("La cuenta importada necesita una clave nueva", "Se detectó que "+why+": el siguiente `terraform apply` rotará la clave (declara `expires_in_days`).")
	}
}

// impliedReads: el servidor guarda expandidas las lecturas que incluye cada scope de escritura.
var impliedReads = map[string][]string{"apps:create": {"apps:read"}, "apps:manage": {"apps:read"}, "quota:manage": {"apps:read"}, "network:manage": {"apps:read"},
	"members:manage": {"apps:read"}, "identities:manage": {"apps:read"}, "queues:manage": {"queues:read"}, "runners:manage": {"runners:read"}, "replication:propose": {"replication:read"}, "schedule:manage": {"apps:read"}}

func expandScopes(in []string) []string {
	set := map[string]bool{}
	for _, s := range in {
		set[s] = true
		for _, r := range impliedReads[s] {
			set[r] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}

// minimalScopes quita las lecturas que ya incluye otro scope (la forma que se escribe en la configuración).
func minimalScopes(in []string) []string {
	implied := map[string]bool{}
	for _, s := range in {
		for _, r := range impliedReads[s] {
			implied[r] = true
		}
	}
	var out []string
	for _, s := range in {
		if !implied[s] {
			out = append(out, s)
		}
	}
	return out
}

// ModifyPlan avisa en `terraform plan` de que la clave privada va a quedar en el state.
func (r *platformAccountResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	warnKeyInState(req, resp)
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var st platformAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Clave vencida, por vencer, revocada o inexistente (o no comprobable): fuerza una actualización que ROTA y lo dice en el plan.
	if need, why := keyNeedsRotation(st.KeyExpiresAt, st.ExpiresAt, time.Now()); need {
		resp.Diagnostics.AddWarning("Este apply rotará la clave de la cuenta de plataforma", "Motivo: "+why+". La clave nueva quedará en el state y la anterior dejará de valer tras la gracia del servidor.")
		for _, a := range []string{"key_id", "key_json", "expires_at", "key_expires_at"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, rootAttr(a), types.StringUnknown())...)
		}
	}
}
