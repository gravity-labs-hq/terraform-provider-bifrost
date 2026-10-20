package provider

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

var projectPathRe = regexp.MustCompile(`^[^\s?#/*.][^\s?#*]*/(\*|[^\s?#*]+)$`)

// canonicalIP exige la forma que el servidor devuelve: una IP suelta con su máscara (/32 o /128) y un rango con su dirección de red. Si no, el provider
// guardaría «203.0.113.7» y la API devolvería «203.0.113.7/32»: el plan nunca convergería. /0 se rechaza igual que en el servidor.
type canonicalIPValidator struct{}

func canonicalIP() canonicalIPValidator { return canonicalIPValidator{} }
func (canonicalIPValidator) Description(context.Context) string {
	return "IP o rango en forma canónica (203.0.113.7/32, 203.0.113.0/24, 2001:db8::/32)"
}
func (v canonicalIPValidator) MarkdownDescription(c context.Context) string { return v.Description(c) }
func (canonicalIPValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	v := req.ConfigValue.ValueString()
	p, err := netip.ParsePrefix(v)
	if err != nil || p.Addr().Zone() != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "IP o rango no válido", fmt.Sprintf("%q no es una IP con máscara ni un rango (ej. 203.0.113.7/32 o 203.0.113.0/24).", v))
		return
	}
	if p.Bits() == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Rango demasiado amplio", fmt.Sprintf("%q abre todo internet; deja la lista vacía si no quieres restringir por IP.", v))
		return
	}
	if p.Addr().Is4In6() || p != p.Masked() || p.String() != v {
		resp.Diagnostics.AddAttributeError(req.Path, "IP o rango fuera de la forma canónica", fmt.Sprintf("Escribe %q (el servidor lo normaliza así y el plan nunca convergería con %q).", p.Masked().String(), v))
	}
}

type maxBytesValidator struct{ n int }

func maxBytes(n int) maxBytesValidator { return maxBytesValidator{n} }
func (v maxBytesValidator) Description(context.Context) string {
	return fmt.Sprintf("texto válido de hasta %d bytes", v.n)
}
func (v maxBytesValidator) MarkdownDescription(c context.Context) string { return v.Description(c) }
func (v maxBytesValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if len(req.ConfigValue.ValueString()) > v.n {
		resp.Diagnostics.AddAttributeError(req.Path, "Texto demasiado largo", fmt.Sprintf("Tiene %d bytes y el máximo es %d.", len(req.ConfigValue.ValueString()), v.n))
	}
}

// groupRe es el slug de grupo de una App (mismo criterio que el servidor: apps.GroupSlug).
var groupRe = regexp.MustCompile(`^$|^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]*$`)

type cidrValidator struct{}

func (cidrValidator) Description(context.Context) string {
	return "rango CIDR válido (ej. 10.0.0.0/24)"
}
func (v cidrValidator) MarkdownDescription(c context.Context) string { return v.Description(c) }
func (cidrValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := netip.ParsePrefix(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "CIDR no válido", fmt.Sprintf("%q no es un rango CIDR (ej. 10.0.0.0/24).", req.ConfigValue.ValueString()))
	}
}

// ================= red del entorno =================

var (
	_ resource.Resource                = &networkResource{}
	_ resource.ResourceWithConfigure   = &networkResource{}
	_ resource.ResourceWithImportState = &networkResource{}
)

func NewNetworkResource() resource.Resource { return &networkResource{} }

type networkResource struct{ base }

type networkModel struct {
	ID            types.String `tfsdk:"id"`
	App           types.String `tfsdk:"app"`
	Environment   types.String `tfsdk:"environment"`
	Domains       types.Set    `tfsdk:"domains"`
	Internet      types.Bool   `tfsdk:"internet"`
	InternetPorts types.Set    `tfsdk:"internet_ports"`
	Egress        types.List   `tfsdk:"egress"`
}

var egressObjType = types.ObjectType{AttrTypes: map[string]attr.Type{"cidr": types.StringType, "port": types.Int64Type, "reason": types.StringType}}

type egressModel struct {
	CIDR   types.String `tfsdk:"cidr"`
	Port   types.Int64  `tfsdk:"port"`
	Reason types.String `tfsdk:"reason"`
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Red propia de un entorno (solo administradores): dominios propios, salida a internet y destinos permitidos. **Reemplaza la red entera** del entorno (la API no tiene cambios parciales). " +
			"Destruir el recurso deja la red vacía (sin dominios, sin salida). `internet` y `egress` son excluyentes.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rr()},
			"domains": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Description: "Dominios propios (hostname completo en minúsculas, máx 10, no de la plataforma ni usados por otro entorno).",
				Validators: nil},
			"internet":       schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "El entorno puede abrir conexiones a cualquier destino de internet."},
			"internet_ports": schema.SetAttribute{Optional: true, ElementType: types.Int64Type, Description: "Puertos TCP de salida (1-65535, máx 10); solo con `internet = true`. Sin indicarlos, la API usa 443 y 80."},
			"egress": schema.ListNestedAttribute{Optional: true, Description: "Destinos permitidos por IP (modo «solo destinos»); no se combina con `internet`.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"cidr":   schema.StringAttribute{Required: true, Validators: []validator.String{cidrValidator{}}},
					"port":   schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}},
					"reason": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Para qué es."},
				}}},
		},
	}
}

func (r *networkResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Internet.ValueBool() && !plan.Egress.IsNull() && len(plan.Egress.Elements()) > 0 {
		resp.Diagnostics.AddAttributeError(rootAttr("egress"), "`internet` y `egress` son excluyentes", "La salida está abierta a cualquier destino: quita la lista de destinos o pon internet = false.")
	}
	if !plan.Internet.IsUnknown() && !plan.Internet.ValueBool() && !plan.InternetPorts.IsNull() && len(plan.InternetPorts.Elements()) > 0 {
		resp.Diagnostics.AddAttributeError(rootAttr("internet_ports"), "Puertos sin salida a internet", "`internet_ports` solo tiene sentido con `internet = true`.")
	}
	if !plan.Domains.IsUnknown() {
		for _, d := range setStrings(ctx, plan.Domains) {
			if !domainRe.MatchString(d) {
				resp.Diagnostics.AddAttributeError(rootAttr("domains"), "Dominio no válido", fmt.Sprintf("%q no es un hostname completo en minúsculas (ej. www.ejemplo.cl).", d))
			}
		}
	}
}

func egressToList(api []client.Egress, prior types.List) types.List {
	if len(api) == 0 && prior.IsNull() {
		return types.ListNull(egressObjType)
	}
	vals := make([]attr.Value, 0, len(api))
	for _, e := range api {
		vals = append(vals, types.ObjectValueMust(egressObjType.AttrTypes, map[string]attr.Value{"cidr": types.StringValue(e.CIDR), "port": types.Int64Value(e.Port), "reason": types.StringValue(e.Reason)}))
	}
	return types.ListValueMust(egressObjType, vals)
}

func egressFromList(ctx context.Context, l types.List) []client.Egress {
	out := []client.Egress{}
	if l.IsNull() || l.IsUnknown() {
		return out
	}
	var ms []egressModel
	_ = l.ElementsAs(ctx, &ms, false)
	for _, m := range ms {
		out = append(out, client.Egress{CIDR: m.CIDR.ValueString(), Port: m.Port.ValueInt64(), Reason: m.Reason.ValueString()})
	}
	return out
}

func portsToSet(api []int64, prior types.Set, internet bool) types.Set {
	def := sameSet(api, []int64{443, 80})
	if prior.IsNull() && (len(api) == 0 || def) {
		return types.SetNull(types.Int64Type)
	}
	return intSet(api)
}

func (r *networkResource) fill(m *networkModel, app, env string, n client.Network) {
	m.ID = types.StringValue(app + "/" + env)
	m.App, m.Environment = types.StringValue(app), types.StringValue(env)
	m.Domains = strSet(n.Domains)
	m.Internet = types.BoolValue(n.Internet)
	m.InternetPorts = portsToSet(n.InternetPorts, m.InternetPorts, n.Internet)
	m.Egress = egressToList(n.Egress, m.Egress)
}

func (r *networkResource) apply(ctx context.Context, m *networkModel) error {
	app, env := m.App.ValueString(), m.Environment.ValueString()
	e, err := r.c.SetNetwork(ctx, app, env, client.NetworkReq{Domains: setStrings(ctx, m.Domains), Internet: m.Internet.ValueBool(),
		InternetPorts: setInts(ctx, m.InternetPorts), Egress: egressFromList(ctx, m.Egress)})
	if err != nil {
		return err
	}
	r.fill(m, app, env, e.Network)
	return nil
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "No se pudo aplicar la red del entorno", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "No se pudo aplicar la red del entorno", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, env := st.App.ValueString(), st.Environment.ValueString()
	e, err := r.c.GetEnv(ctx, app, env)
	if client.IsNotFound(err) || (err == nil && e.Status == client.StatusArchived) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la red del entorno", err)
		return
	}
	r.fill(&st, app, env, e.Network)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.c.SetNetwork(ctx, st.App.ValueString(), st.Environment.ValueString(), client.NetworkReq{Domains: []string{}, InternetPorts: []int64{}, Egress: []client.Egress{}})
	if err != nil && !client.IsNotFound(err) && !client.IsConflict(err) { // 409: entorno archivado, nada que cerrar
		apiError(&resp.Diagnostics, "No se pudo vaciar la red del entorno", err)
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/entorno", &resp.Diagnostics)
	if p == nil {
		return
	}
	e, err := r.c.GetEnv(ctx, p[0], p[1])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la red", err)
		return
	}
	m := networkModel{InternetPorts: types.SetNull(types.Int64Type), Egress: types.ListNull(egressObjType)}
	r.fill(&m, p[0], p[1], e.Network)
	if len(e.Network.InternetPorts) > 0 {
		m.InternetPorts = intSet(e.Network.InternetPorts)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= reglas por dirección (host) =================

var (
	_ resource.Resource                   = &hostRulesResource{}
	_ resource.ResourceWithConfigure      = &hostRulesResource{}
	_ resource.ResourceWithImportState    = &hostRulesResource{}
	_ resource.ResourceWithValidateConfig = &hostRulesResource{}
)

func NewHostRulesResource() resource.Resource { return &hostRulesResource{} }

type hostRulesResource struct{ base }

type rateLimitModel struct {
	Average       types.Int64 `tfsdk:"average"`
	PeriodSeconds types.Int64 `tfsdk:"period_seconds"`
	Burst         types.Int64 `tfsdk:"burst"`
}

type basicUserModel struct {
	Name     types.String `tfsdk:"name"`
	Password types.String `tfsdk:"password"`
}

type corsModel struct {
	Origins       types.Set   `tfsdk:"origins"`
	Methods       types.Set   `tfsdk:"methods"`
	Headers       types.Set   `tfsdk:"headers"`
	Credentials   types.Bool  `tfsdk:"credentials"`
	MaxAgeSeconds types.Int64 `tfsdk:"max_age_seconds"`
}

type hostRulesModel struct {
	ID              types.String      `tfsdk:"id"`
	App             types.String      `tfsdk:"app"`
	Environment     types.String      `tfsdk:"environment"`
	Host            types.String      `tfsdk:"host"`
	IPAllow         types.Set         `tfsdk:"ip_allow"`
	IPDeny          types.Set         `tfsdk:"ip_deny"`
	RateLimit       *rateLimitModel   `tfsdk:"rate_limit"`
	BasicAuth       []basicUserModel  `tfsdk:"basic_auth"`
	WWW             types.String      `tfsdk:"www"`
	SecurityHeaders types.Bool        `tfsdk:"security_headers"`
	Compress        types.Bool        `tfsdk:"compress"`
	MaxBodyMB       types.Int64       `tfsdk:"max_body_mb"`
	CORS            *corsModel        `tfsdk:"cors"`
	IPNotes         types.Map         `tfsdk:"ip_notes"`
	HTTPSRedirect   types.Bool        `tfsdk:"https_redirect"`
	TimeoutSeconds  types.Int64       `tfsdk:"timeout_seconds"`
	Maintenance     *maintenanceModel `tfsdk:"maintenance"`
}

type maintenanceModel struct {
	HTML              types.String `tfsdk:"html"`
	RetryAfterSeconds types.Int64  `tfsdk:"retry_after_seconds"`
	AllowIPs          types.Set    `tfsdk:"allow_ips"`
}

func (r *hostRulesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host_rules"
}

func (r *hostRulesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	zero := int64default.StaticInt64(0)
	resp.Schema = schema.Schema{
		Description: "Reglas de una dirección (host) de un entorno: listas de IP, límite de peticiones, autenticación básica, CORS y cabeceras (solo administradores). Reemplaza todas las reglas del host. " +
			"La API nunca devuelve contraseñas: no hay detección de drift de `basic_auth.password`. Destruir deja el host sin reglas.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rr()},
			"host":        schema.StringAttribute{Required: true, Description: "Hostname del entorno (el automático o un dominio propio).", PlanModifiers: rr()},
			"ip_allow": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Validators: []validator.Set{setvalidator.SizeAtMost(200), setvalidator.ValueStringsAre(canonicalIP())},
				Description: "IP/CIDR permitidas (vacío = todas). Máximo 200, en forma canónica: una IP suelta se escribe con su máscara (`203.0.113.7/32`) y un rango con su dirección de red (`203.0.113.0/24`), porque el servidor las normaliza así."},
			"ip_deny": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Validators: []validator.Set{setvalidator.SizeAtMost(1000), setvalidator.ValueStringsAre(canonicalIP())},
				Description: "IP/CIDR bloqueadas (máximo 1000, misma forma canónica que `ip_allow`)."},
			"ip_notes": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptyStringMap,
				Validators:  []validator.Map{mapvalidator.KeysAre(canonicalIP()), mapvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 120))},
				Description: "Nota (el motivo, hasta 120 caracteres) de cada entrada de `ip_allow`/`ip_deny`, por entrada canónica. Una nota de una entrada que no está en las listas se rechaza. Quién y cuándo la puso lo anota el servidor y no se maneja aquí."},
			"rate_limit": schema.SingleNestedAttribute{Optional: true, Description: "Límite de peticiones.", Attributes: map[string]schema.Attribute{
				"average":        schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)}},
				"period_seconds": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)}},
				"burst":          schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)}},
			}},
			"basic_auth": schema.ListNestedAttribute{Optional: true, Description: "Usuarios de autenticación básica.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"name":     schema.StringAttribute{Required: true},
				"password": schema.StringAttribute{Required: true, Sensitive: true, Description: "Solo de entrada: la API no la devuelve (y queda en el state)."},
			}}},
			"www":              schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Redirección www (según el portal; vacío = ninguna)."},
			"security_headers": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"compress":         schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"max_body_mb":      schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Description: "Tamaño máximo del cuerpo (MB); 0 = sin límite."},
			"cors": schema.SingleNestedAttribute{Optional: true, Description: "Reglas CORS.", Attributes: map[string]schema.Attribute{
				"origins":         schema.SetAttribute{Required: true, ElementType: types.StringType},
				"methods":         schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet},
				"headers":         schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet},
				"credentials":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
				"max_age_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: zero},
			}},
			"https_redirect": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Quien entra por `http://` pasa a `https://`."},
			"timeout_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Validators: []validator.Int64{int64validator.Between(0, 3600)},
				Description: "Espera máxima (s) de las cabeceras de respuesta de la App; al pasarse, 504. 0 = el predeterminado de Traefik. Necesita el transporte por dirección en el cluster (si no, se guarda pero no se aplica y el provider avisa)."},
			"maintenance": schema.SingleNestedAttribute{Optional: true, Description: "Página de mantenimiento: mientras exista, quien no esté en `allow_ips` recibe 503 con este HTML (la App sigue corriendo). Quitar el bloque la apaga. Necesita el plugin de mantenimiento en el cluster.", Attributes: map[string]schema.Attribute{
				"html":                schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Validators: []validator.String{maxBytes(4096)}, Description: "HTML de la página (hasta 4096 bytes); vacío = la página por omisión del plugin."},
				"retry_after_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Validators: []validator.Int64{int64validator.Between(0, 86400)}, Description: "Cabecera `Retry-After` (s); 0 = sin cabecera."},
				"allow_ips":           schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Validators: []validator.Set{setvalidator.SizeAtMost(50), setvalidator.ValueStringsAre(canonicalIP())}, Description: "IP/CIDR que se saltan la página (el equipo que verifica la App); máximo 50, forma canónica."},
			}},
		},
	}
}

func (r *hostRulesResource) fill(ctx context.Context, m *hostRulesModel, app, env, host string, rules client.HostRules) {
	m.ID = types.StringValue(app + "/" + env + "/" + host)
	m.App, m.Environment, m.Host = types.StringValue(app), types.StringValue(env), types.StringValue(host)
	m.IPAllow, m.IPDeny = strSet(rules.IPAllow), strSet(rules.IPDeny)
	m.WWW, m.SecurityHeaders, m.Compress, m.MaxBodyMB = types.StringValue(rules.WWW), types.BoolValue(rules.SecurityHeaders), types.BoolValue(rules.Compress), types.Int64Value(rules.MaxBodyMB)
	m.RateLimit = nil
	if rl := rules.RateLimit; rl != nil {
		m.RateLimit = &rateLimitModel{Average: types.Int64Value(rl.Average), PeriodSeconds: types.Int64Value(rl.PeriodSeconds), Burst: types.Int64Value(rl.Burst)}
	}
	// Las contraseñas no vuelven de la API: se conservan las del state/plan por nombre.
	prior := map[string]types.String{}
	for _, u := range m.BasicAuth {
		prior[u.Name.ValueString()] = u.Password
	}
	if len(rules.BasicAuth) == 0 && m.BasicAuth == nil {
		m.BasicAuth = nil
	} else {
		m.BasicAuth = []basicUserModel{}
		for _, u := range rules.BasicAuth {
			pw, ok := prior[u.Name]
			if !ok {
				pw = types.StringValue("")
			}
			m.BasicAuth = append(m.BasicAuth, basicUserModel{Name: types.StringValue(u.Name), Password: pw})
		}
	}
	m.CORS = nil
	if c := rules.CORS; c != nil {
		m.CORS = &corsModel{Origins: strSet(c.Origins), Methods: strSet(c.Methods), Headers: strSet(c.Headers), Credentials: types.BoolValue(c.Credentials), MaxAgeSeconds: types.Int64Value(c.MaxAgeSeconds)}
	}
	notes := map[string]string{}
	for ip, n := range rules.IPNotes {
		if n.Note != "" {
			notes[ip] = n.Note
		}
	}
	m.IPNotes = strMap(notes)
	m.HTTPSRedirect, m.TimeoutSeconds = types.BoolValue(rules.HTTPSRedirect), types.Int64Value(rules.TimeoutSeconds)
	m.Maintenance = nil
	if mt := rules.Maintenance; mt != nil {
		m.Maintenance = &maintenanceModel{HTML: types.StringValue(mt.HTML), RetryAfterSeconds: types.Int64Value(mt.RetryAfterSeconds), AllowIPs: strSet(mt.AllowIPs)}
	}
}

func (r *hostRulesResource) toAPI(ctx context.Context, m *hostRulesModel) client.HostRules {
	// PUT reemplaza TODAS las reglas del host: se envían completas (incluidas las de la ola 1) para que aplicar no apague nada en silencio.
	h := client.HostRules{IPAllow: setStrings(ctx, m.IPAllow), IPDeny: setStrings(ctx, m.IPDeny), WWW: m.WWW.ValueString(),
		SecurityHeaders: m.SecurityHeaders.ValueBool(), Compress: m.Compress.ValueBool(), MaxBodyMB: m.MaxBodyMB.ValueInt64(), BasicAuth: []client.BasicUser{},
		HTTPSRedirect: m.HTTPSRedirect.ValueBool(), TimeoutSeconds: m.TimeoutSeconds.ValueInt64(), IPNotes: map[string]client.IPNote{}}
	for ip, n := range mapStrings(ctx, m.IPNotes) {
		h.IPNotes[ip] = client.IPNote{Note: n}
	}
	if mt := m.Maintenance; mt != nil {
		h.Maintenance = &client.Maintenance{HTML: mt.HTML.ValueString(), RetryAfterSeconds: mt.RetryAfterSeconds.ValueInt64(), AllowIPs: setStrings(ctx, mt.AllowIPs)}
	}
	if rl := m.RateLimit; rl != nil {
		h.RateLimit = &client.RateLimit{Average: rl.Average.ValueInt64(), PeriodSeconds: rl.PeriodSeconds.ValueInt64(), Burst: rl.Burst.ValueInt64()}
	}
	for _, u := range m.BasicAuth {
		h.BasicAuth = append(h.BasicAuth, client.BasicUser{Name: u.Name.ValueString(), Password: u.Password.ValueString()})
	}
	if c := m.CORS; c != nil {
		h.CORS = &client.CORS{Origins: setStrings(ctx, c.Origins), Methods: setStrings(ctx, c.Methods), Headers: setStrings(ctx, c.Headers), Credentials: c.Credentials.ValueBool(), MaxAgeSeconds: c.MaxAgeSeconds.ValueInt64()}
	}
	return h
}

func (r *hostRulesResource) put(ctx context.Context, m *hostRulesModel, diags *diag.Diagnostics) error {
	app, env, host := m.App.ValueString(), m.Environment.ValueString(), m.Host.ValueString()
	v, err := r.c.SetHostRules(ctx, app, env, host, r.toAPI(ctx, m))
	if err != nil {
		return err
	}
	if m.Maintenance != nil && !v.MaintenanceAvailable {
		diags.AddWarning("La página de mantenimiento no se aplica", "El cluster no tiene el plugin de mantenimiento: las reglas se guardaron pero la página no se sirve hasta que se instale.")
	}
	if m.TimeoutSeconds.ValueInt64() > 0 && !v.TimeoutAvailable {
		diags.AddWarning("El tiempo de espera no se aplica", "El cluster no tiene el transporte por dirección (serverstransports): el valor se guardó pero no se aplica.")
	}
	if len(setStrings(ctx, m.IPDeny)) > 0 && !v.DenyAvailable {
		diags.AddWarning("La lista negra no se aplica", "El cluster no tiene el plugin de lista negra: las IP bloqueadas se guardaron pero no se aplican.")
	}
	r.fill(ctx, m, app, env, host, v.Rules)
	return nil
}

// ValidateConfig comprueba lo que el servidor descartaría en silencio y produciría un plan que nunca converge: una nota de una IP que no está en las listas.
func (r *hostRulesResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var allow, deny, notes types.Set
	var nm types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ip_allow"), &allow)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ip_deny"), &deny)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ip_notes"), &nm)...)
	_ = notes
	if resp.Diagnostics.HasError() || nm.IsNull() || nm.IsUnknown() || allow.IsUnknown() || deny.IsUnknown() {
		return
	}
	have := map[string]bool{}
	for _, l := range [][]string{setStrings(ctx, allow), setStrings(ctx, deny)} {
		for _, x := range l {
			have[x] = true
		}
	}
	for ip := range nm.Elements() {
		if !have[ip] {
			resp.Diagnostics.AddAttributeError(path.Root("ip_notes"), "Nota de una IP que no está en las listas", fmt.Sprintf("%q no aparece en `ip_allow` ni en `ip_deny`: el servidor descartaría la nota.", ip))
		}
	}
}

func (r *hostRulesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan hostRulesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "No se pudieron guardar las reglas del host", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *hostRulesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan hostRulesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan, &resp.Diagnostics); err != nil {
		apiError(&resp.Diagnostics, "No se pudieron guardar las reglas del host", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *hostRulesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st hostRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, env, host := st.App.ValueString(), st.Environment.ValueString(), st.Host.ValueString()
	v, err := r.c.GetHostRules(ctx, app, env, host)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudieron leer las reglas del host", err)
		return
	}
	r.fill(ctx, &st, app, env, host, v.Rules)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *hostRulesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st hostRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	empty := client.HostRules{IPAllow: []string{}, IPDeny: []string{}, IPNotes: map[string]client.IPNote{}, BasicAuth: []client.BasicUser{}}
	if _, err := r.c.SetHostRules(ctx, st.App.ValueString(), st.Environment.ValueString(), st.Host.ValueString(), empty); err != nil && !client.IsNotFound(err) && !client.IsConflict(err) {
		apiError(&resp.Diagnostics, "No se pudieron vaciar las reglas del host", err)
	}
}

func (r *hostRulesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 3, "app/entorno/host", &resp.Diagnostics)
	if p == nil {
		return
	}
	v, err := r.c.GetHostRules(ctx, p[0], p[1], p[2])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudieron importar las reglas", err)
		return
	}
	var m hostRulesModel
	r.fill(ctx, &m, p[0], p[1], p[2], v.Rules)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if len(v.Rules.BasicAuth) > 0 {
		resp.Diagnostics.AddWarning("Contraseñas desconocidas", "La API no devuelve contraseñas: indica `basic_auth.password` en tu configuración y se reaplicarán en el siguiente apply.")
	}
}

var _ = stringvalidator.OneOf
