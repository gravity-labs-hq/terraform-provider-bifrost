package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

// ================= salidas a internet por workload =================

var (
	_ resource.Resource                   = &wlEgressResource{}
	_ resource.ResourceWithConfigure      = &wlEgressResource{}
	_ resource.ResourceWithImportState    = &wlEgressResource{}
	_ resource.ResourceWithValidateConfig = &wlEgressResource{}
)

func NewWorkloadEgressResource() resource.Resource { return &wlEgressResource{} }

type wlEgressResource struct{ base }

type wlEgressModel struct {
	ID          types.String            `tfsdk:"id"`
	App         types.String            `tfsdk:"app"`
	Environment types.String            `tfsdk:"environment"`
	Rules       map[string]wlEgressItem `tfsdk:"rules"`
	Version     types.String            `tfsdk:"version"`
}

type wlEgressItem struct {
	Protocol types.String         `tfsdk:"protocol"`
	Port     types.Int64          `tfsdk:"port"`
	Target   *wlEgressTargetModel `tfsdk:"target"`
	State    types.String         `tfsdk:"state"`
	Message  types.String         `tfsdk:"message"`
	Policy   types.String         `tfsdk:"policy"`
}

type wlEgressTargetModel struct {
	Kind types.String `tfsdk:"kind"`
	Name types.String `tfsdk:"name"`
}

// deniedEgressPorts es el suelo de puertos de destino que el servidor nunca abre (bifrost/docs/egress.md): se comprueba en el plan para fallar antes.
var deniedEgressPorts = map[int64]bool{20: true, 21: true, 22: true, 23: true, 53: true, 69: true, 111: true, 135: true, 136: true, 137: true, 138: true, 139: true, 161: true, 389: true,
	445: true, 514: true, 873: true, 1433: true, 1521: true, 2049: true, 2375: true, 2376: true, 3306: true, 3389: true, 5432: true, 5900: true, 5901: true, 5902: true, 5903: true, 5904: true,
	5905: true, 6379: true, 9100: true, 9200: true, 11211: true, 27017: true, 2379: true, 2380: true, 6443: true, 6444: true, 8472: true, 9345: true, 51820: true, 51821: true}

func (r *wlEgressResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_egress"
}

func (r *wlEgressResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Salida a internet por puerto ACOTADA A UN WORKLOAD de UN entorno: Bifröst crea por cada regla una NetworkPolicy `egress-<nombre>` que permite TCP al puerto hacia internet (salvo los rangos privados) SOLO a los pods de un deployment o statefulset. " +
			"`internet_ports` del entorno vale para TODOS los pods; esto abre p. ej. el 25 solo para el servidor de correo, sin que un pod comprometido (API, worker, SSRF) envíe SMTP directo desde la IP del servidor. " +
			"**Es AUTORITATIVO**: la lista `rules` reemplaza TODAS las reglas del entorno (una regla creada por otra vía aparece como deriva y el siguiente apply la revoca) y destruir el recurso las revoca todas. Un recurso por App y entorno. " +
			"Solo una persona administradora o una cuenta de plataforma con el scope `ports:manage` (y `allow_ports`) sobre la App. Nunca se abren 22, 23, 53, 135-139, 445, 3389, bases de datos ni los puertos del propio servidor.",
		Attributes: map[string]schema.Attribute{
			"id":          idAttr(),
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rr()},
			"version":     schema.StringAttribute{Computed: true, Description: "Versión de la lista guardada (vacía si no hay reglas). La usa el provider para no pisar cambios ajenos."},
			"rules": schema.MapNestedAttribute{Required: true,
				Validators:  []validator.Map{mapvalidator.SizeAtMost(5), mapvalidator.KeysAre(stringvalidator.RegexMatches(portNameRe, "minúsculas, números y guiones; empieza con letra; hasta 30"))},
				Description: "Reglas por nombre (minúsculas, números y guiones; empieza con letra; hasta 30 caracteres; máximo 5 por entorno). La política se llama `egress-<nombre>`. `{}` revoca todas.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"protocol": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("tcp")}, Description: "Solo `tcp`."},
					"port":     schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}, Description: "Puerto de destino en internet (el 25 se puede abrir). Los puertos de administración, archivos, bases de datos y los del propio servidor están prohibidos."},
					"target": schema.SingleNestedAttribute{Required: true, Description: "Workload cuyos pods reciben la regla. El selector sale de `matchLabels` del workload; si otro workload del entorno también lo cumple, el servidor lo rechaza.",
						Attributes: map[string]schema.Attribute{
							"kind": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("deployment", "statefulset")}, Description: "`deployment` o `statefulset`."},
							"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 63)}, Description: "Nombre del workload."},
						}},
					"state":   schema.StringAttribute{Computed: true, Description: "`applied`, `pending`, `error` o `closed` (entorno archivado)."},
					"message": schema.StringAttribute{Computed: true, Description: "Detalle del estado, si lo hay."},
					"policy":  schema.StringAttribute{Computed: true, Description: "Nombre de la NetworkPolicy (`egress-<nombre>`)."},
				}}},
		},
	}
}

// ValidateConfig falla en el plan por lo que el servidor rechazaría: puertos prohibidos y reglas repetidas (mismo workload y puerto).
func (r *wlEgressResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var rules types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("rules"), &rules)...)
	if resp.Diagnostics.HasError() || rules.IsNull() || rules.IsUnknown() {
		return
	}
	seen := map[string]string{}
	elems := rules.Elements()
	names := make([]string, 0, len(elems))
	for n := range elems {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		obj, ok := elems[n].(types.Object)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			continue
		}
		at := obj.Attributes()
		port, _ := at["port"].(types.Int64)
		if port.IsNull() || port.IsUnknown() {
			continue
		}
		v := port.ValueInt64()
		if deniedEgressPorts[v] || (v >= 10250 && v <= 10259) || (v >= 30000 && v <= 32767) {
			resp.Diagnostics.AddError("Puerto prohibido", fmt.Sprintf("El puerto %d de %q está prohibido (administración, archivos, bases de datos o del propio servidor) y nunca se abre.", v, n))
		}
		if t, ok := at["target"].(types.Object); ok && !t.IsNull() && !t.IsUnknown() {
			k, _ := t.Attributes()["kind"].(types.String)
			nm, _ := t.Attributes()["name"].(types.String)
			if !k.IsUnknown() && !nm.IsUnknown() {
				key := fmt.Sprintf("%s/%s:%d", k.ValueString(), nm.ValueString(), v)
				if o, dup := seen[key]; dup {
					resp.Diagnostics.AddError("Regla repetida", fmt.Sprintf("%q y %q permiten el puerto %d al mismo workload.", o, n, v))
				}
				seen[key] = n
			}
		}
	}
}

func (r *wlEgressResource) specs(m *wlEgressModel) []client.EgressSpec {
	names := make([]string, 0, len(m.Rules))
	for n := range m.Rules {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]client.EgressSpec, 0, len(names))
	for _, n := range names {
		x := m.Rules[n]
		out = append(out, client.EgressSpec{Name: n, Protocol: x.Protocol.ValueString(), Port: x.Port.ValueInt64(),
			Target: client.EgressTarget{Kind: x.Target.Kind.ValueString(), Name: x.Target.Name.ValueString()}})
	}
	return out
}

func (r *wlEgressResource) fill(m *wlEgressModel, app, env string, v client.EgressView) {
	m.ID, m.App, m.Environment, m.Version = types.StringValue(app+"/"+env), types.StringValue(app), types.StringValue(env), types.StringValue(v.Version)
	m.Rules = map[string]wlEgressItem{}
	for _, x := range v.Rules {
		m.Rules[x.Name] = wlEgressItem{Protocol: types.StringValue(x.Protocol), Port: types.Int64Value(x.Port),
			Target: &wlEgressTargetModel{Kind: types.StringValue(x.Target.Kind), Name: types.StringValue(x.Target.Name)},
			State:  types.StringValue(x.State), Message: types.StringValue(x.Message), Policy: types.StringValue(x.Policy)}
	}
}

// apply lee la versión vigente y reemplaza la lista; si otro cambió la lista entre las dos llamadas, relee y reintenta (acotado).
func (r *wlEgressResource) apply(ctx context.Context, app, env string, specs []client.EgressSpec) (client.EgressView, error) {
	var v client.EgressView
	var err error
	for i := 0; i < 4; i++ {
		cur, gerr := r.c.GetEgress(ctx, app, env)
		if gerr != nil {
			return v, gerr
		}
		if v, err = r.c.SetEgress(ctx, app, env, client.SetEgressReq{Version: cur.Version, Rules: specs}); !versionConflict(err) {
			return v, err
		}
	}
	return v, err
}

func wlEgressError(diags *diag.Diagnostics, summary string, err error) {
	switch {
	case versionConflict(err):
		diags.AddError(summary, err.Error()+"\n\nOtra persona o proceso cambió las salidas del entorno mientras se aplicaba. Ejecuta `terraform apply` de nuevo.")
	case client.IsForbidden(err):
		diags.AddError(summary, err.Error()+"\n\nAbrir salidas solo lo puede hacer una persona administradora o una cuenta de plataforma con el scope `ports:manage` (creada con `allow_ports = true`) sobre esta App.")
	default:
		apiError(diags, summary, err)
	}
}

func (r *wlEgressResource) put(ctx context.Context, m *wlEgressModel, diags *diag.Diagnostics) bool {
	app, env := m.App.ValueString(), m.Environment.ValueString()
	v, err := r.apply(ctx, app, env, r.specs(m))
	if err != nil {
		wlEgressError(diags, "No se pudieron guardar las salidas", err)
		return false
	}
	r.fill(m, app, env, v)
	return true
}

func (r *wlEgressResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan wlEgressModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *wlEgressResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan wlEgressModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *wlEgressResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st wlEgressModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, env := st.App.ValueString(), st.Environment.ValueString()
	v, err := r.c.GetEgress(ctx, app, env)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		wlEgressError(&resp.Diagnostics, "No se pudieron leer las salidas", err)
		return
	}
	r.fill(&st, app, env, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Delete revoca TODAS las reglas del entorno (el recurso es autoritativo).
func (r *wlEgressResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st wlEgressModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.apply(ctx, st.App.ValueString(), st.Environment.ValueString(), []client.EgressSpec{}); err != nil && !client.IsNotFound(err) {
		wlEgressError(&resp.Diagnostics, "No se pudieron revocar las salidas", err)
	}
}

func (r *wlEgressResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/entorno", &resp.Diagnostics)
	if p == nil {
		return
	}
	v, err := r.c.GetEgress(ctx, p[0], p[1])
	if err != nil {
		wlEgressError(&resp.Diagnostics, "No se pudieron importar las salidas", err)
		return
	}
	if len(v.Rules) == 0 {
		resp.Diagnostics.AddError("No hay salidas por workload", fmt.Sprintf("El entorno %s/%s no tiene reglas de salida: declara el recurso y aplica en vez de importarlo.", p[0], p[1]))
		return
	}
	var m wlEgressModel
	r.fill(&m, p[0], p[1], v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
