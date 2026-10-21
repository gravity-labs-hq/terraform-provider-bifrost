package provider

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
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

// ================= puertos públicos TCP/UDP =================

var (
	_ resource.Resource                   = &portsResource{}
	_ resource.ResourceWithConfigure      = &portsResource{}
	_ resource.ResourceWithImportState    = &portsResource{}
	_ resource.ResourceWithValidateConfig = &portsResource{}
)

func NewPortsResource() resource.Resource { return &portsResource{} }

type portsResource struct{ base }

type portsModel struct {
	ID          types.String        `tfsdk:"id"`
	App         types.String        `tfsdk:"app"`
	Environment types.String        `tfsdk:"environment"`
	Ports       map[string]portItem `tfsdk:"ports"`
	Version     types.String        `tfsdk:"version"`
}

type portItem struct {
	Protocol   types.String     `tfsdk:"protocol"`
	Port       types.Int64      `tfsdk:"port"`
	Target     *portTargetModel `tfsdk:"target"`
	AllowCIDRs types.Set        `tfsdk:"allow_cidrs"`
	State      types.String     `tfsdk:"state"`
	LBReady    types.Bool       `tfsdk:"lb_ready"`
	Addresses  types.Set        `tfsdk:"addresses"`
	Message    types.String     `tfsdk:"message"`
	Service    types.String     `tfsdk:"service"`
}

type portTargetModel struct {
	Kind types.String `tfsdk:"kind"`
	Name types.String `tfsdk:"name"`
	Port types.Int64  `tfsdk:"port"`
}

var portNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$`)

// reservedPorts es el suelo de puertos que el servidor nunca abre (docs/puertos.md): se comprueba en el plan para fallar antes. El servidor puede reservar más.
var reservedPorts = map[int64]bool{22: true, 53: true, 80: true, 111: true, 443: true, 2222: true, 2379: true, 2380: true, 6443: true, 8472: true, 9100: true, 9345: true,
	10250: true, 10256: true, 10257: true, 10259: true, 51820: true, 51821: true}

func (r *portsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ports"
}

func (r *portsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Puertos públicos TCP/UDP de UN entorno (correo SMTP/IMAP, juegos…): Bifröst crea por cada puerto un Service `LoadBalancer` (ServiceLB de K3s) hacia los pods de un workload del entorno y una NetworkPolicy que solo admite ese puerto. " +
			"**Es AUTORITATIVO**: la lista `ports` reemplaza TODOS los puertos del entorno (un puerto abierto por otra vía aparece como deriva y el siguiente apply lo cierra) y destruir el recurso cierra todos los puertos. " +
			"Un recurso por App y entorno (no uno por puerto): la API guarda la lista completa con control de versión y varios recursos en paralelo se pisarían. " +
			"**Abrir un puerto expone un servicio en la IP del servidor, fuera de Cloudflare.** Solo lo puede hacer una persona administradora o una cuenta de plataforma con el scope `ports:manage` (y `allow_ports`) sobre la App. " +
			"El puerto no puede estar reservado (22, 80, 443, 2222, 6443, 10250, 9100, 30000-32767…) ni usado por otro entorno u otra App (409), y cada App tiene un máximo de puertos (10 por omisión).",
		Attributes: map[string]schema.Attribute{
			"id":          idAttr(),
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rr()},
			"version":     schema.StringAttribute{Computed: true, Description: "Versión de la lista guardada (cambia con cualquier edición; vacía si no hay puertos). La usa el provider para no pisar cambios ajenos."},
			"ports": schema.MapNestedAttribute{Required: true,
				Validators:  []validator.Map{mapvalidator.SizeAtMost(100), mapvalidator.KeysAre(stringvalidator.RegexMatches(portNameRe, "minúsculas, números y guiones; empieza con letra; hasta 30"))},
				Description: "Puertos por nombre (minúsculas, números y guiones; empieza con letra; hasta 30 caracteres). El nombre es la identidad del puerto: va en la etiqueta `gravity.io/port` y el Service se llama `bfp-<nombre>`. `{}` cierra todos los puertos.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"protocol": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("tcp", "udp")}, Description: "`tcp` o `udp`."},
					"port":     schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}, Description: "Puerto público (1-65535). No puede estar reservado ni usado por otro entorno u otra App."},
					"target": schema.SingleNestedAttribute{Required: true, Description: "Workload del entorno al que va el tráfico. El selector del Service se copia de `matchLabels` del workload; si otro workload del entorno también lo cumple, el servidor lo rechaza.",
						Attributes: map[string]schema.Attribute{
							"kind": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("deployment", "statefulset")}, Description: "`deployment` o `statefulset`."},
							"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 63)}, Description: "Nombre del workload."},
							"port": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}, Description: "Puerto del contenedor."},
						}},
					"allow_cidrs": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Validators: []validator.Set{setvalidator.SizeAtMost(50)},
						Description: "Lista blanca de rangos (máx. 50) en forma canónica CIDR (`203.0.113.0/24`, `198.51.100.7/32`). Vacía = abierto a todo internet. Sin rangos internos ni `/0`. Se aplica en ServiceLB (`loadBalancerSourceRanges`) y en la NetworkPolicy. **Recomendada, sobre todo en UDP.**"},
					"state":     schema.StringAttribute{Computed: true, Description: "`open`, `pending` (el svclb aún no está listo), `conflict`, `error` o `closed` (entorno archivado)."},
					"lb_ready":  schema.BoolAttribute{Computed: true, Description: "ServiceLB ya abrió el puerto en el nodo."},
					"addresses": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "IP del nodo donde ServiceLB publicó el puerto."},
					"message":   schema.StringAttribute{Computed: true, Description: "Detalle del estado, si lo hay."},
					"service":   schema.StringAttribute{Computed: true, Description: "Nombre del Service creado (`bfp-<nombre>`)."},
				}}},
		},
	}
}

// ValidateConfig falla en el plan por lo que el servidor rechazaría: puertos reservados, repetidos o rangos que no están en forma canónica (el servidor los
// normaliza y el plan nunca convergería).
func (r *portsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var ports types.Map
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("ports"), &ports)...)
	if resp.Diagnostics.HasError() || ports.IsNull() || ports.IsUnknown() {
		return
	}
	seen := map[string]string{}
	elems := ports.Elements()
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
		proto, _ := at["protocol"].(types.String)
		if !port.IsNull() && !port.IsUnknown() {
			v := port.ValueInt64()
			if reservedPorts[v] || (v >= 30000 && v <= 32767) {
				resp.Diagnostics.AddError("Puerto reservado", fmt.Sprintf("El puerto %d de %q está reservado por la plataforma (SSH, web, Kubernetes, NodePort…) y nunca se abre.", v, n))
			}
			if !proto.IsNull() && !proto.IsUnknown() {
				k := fmt.Sprintf("%s/%d", proto.ValueString(), v)
				if o, dup := seen[k]; dup {
					resp.Diagnostics.AddError("Puerto repetido", fmt.Sprintf("%s lo declaran %q y %q.", k, o, n))
				}
				seen[k] = n
			}
		}
		cidrs, _ := at["allow_cidrs"].(types.Set)
		if cidrs.IsNull() || cidrs.IsUnknown() {
			continue
		}
		for _, c := range setStrings(ctx, cidrs) {
			pf, err := netip.ParsePrefix(c)
			if err != nil || pf != pf.Masked() || pf.String() != c {
				resp.Diagnostics.AddError("Rango no canónico", fmt.Sprintf("allow_cidrs de %q: %q debe escribirse como CIDR canónico (p. ej. 203.0.113.0/24 o 198.51.100.7/32, sin bits de host); el servidor lo normaliza y el plan no convergería.", n, c))
			}
		}
	}
}

func (r *portsResource) specs(ctx context.Context, m *portsModel) []client.PortSpec {
	names := make([]string, 0, len(m.Ports))
	for n := range m.Ports {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]client.PortSpec, 0, len(names))
	for _, n := range names {
		p := m.Ports[n]
		cidrs := setStrings(ctx, p.AllowCIDRs)
		if cidrs == nil {
			cidrs = []string{}
		}
		sort.Strings(cidrs)
		out = append(out, client.PortSpec{Name: n, Protocol: p.Protocol.ValueString(), Port: p.Port.ValueInt64(), AllowCIDRs: cidrs,
			Target: client.PortTarget{Kind: p.Target.Kind.ValueString(), Name: p.Target.Name.ValueString(), Port: p.Target.Port.ValueInt64()}})
	}
	return out
}

func (r *portsResource) fill(m *portsModel, app, env string, v client.PortsView) {
	m.ID, m.App, m.Environment, m.Version = types.StringValue(app+"/"+env), types.StringValue(app), types.StringValue(env), types.StringValue(v.Version)
	m.Ports = map[string]portItem{}
	for _, p := range v.Ports {
		m.Ports[p.Name] = portItem{Protocol: types.StringValue(p.Protocol), Port: types.Int64Value(p.Port),
			Target:     &portTargetModel{Kind: types.StringValue(p.Target.Kind), Name: types.StringValue(p.Target.Name), Port: types.Int64Value(p.Target.Port)},
			AllowCIDRs: strSet(p.AllowCIDRs), State: types.StringValue(p.State), LBReady: types.BoolValue(p.LBReady), Addresses: strSet(p.Addresses),
			Message: types.StringValue(p.Message), Service: types.StringValue(p.Service)}
	}
}

// versionConflict distingue el 409 de versión (se reintenta con la versión nueva) del 409 de colisión de puerto (no se reintenta).
func versionConflict(err error) bool {
	return client.IsConflict(err) && strings.Contains(err.Error(), "cambiaron")
}

// apply lee la versión vigente y reemplaza la lista; si otro cambió la lista entre las dos llamadas, relee y reintenta (acotado).
func (r *portsResource) apply(ctx context.Context, app, env string, specs []client.PortSpec) (client.PortsView, error) {
	var v client.PortsView
	var err error
	for i := 0; i < 4; i++ {
		cur, gerr := r.c.GetPorts(ctx, app, env)
		if gerr != nil {
			return v, gerr
		}
		if v, err = r.c.SetPorts(ctx, app, env, client.SetPortsReq{Version: cur.Version, Ports: specs}); !versionConflict(err) {
			return v, err
		}
	}
	return v, err
}

func portsError(diags *diag.Diagnostics, summary string, err error) {
	switch {
	case versionConflict(err):
		diags.AddError(summary, err.Error()+"\n\nOtra persona o proceso cambió los puertos del entorno mientras se aplicaba. Ejecuta `terraform apply` de nuevo.")
	case client.IsConflict(err):
		diags.AddError("Puerto en uso", err.Error()+"\n\nEl puerto ya lo usa otro entorno, otra App o un Service del cluster: elige otro (el conflicto no se reintenta).")
	case client.IsForbidden(err):
		diags.AddError(summary, err.Error()+"\n\nAbrir puertos solo lo puede hacer una persona administradora o una cuenta de plataforma con el scope `ports:manage` (creada con `allow_ports = true`) sobre esta App.")
	default:
		apiError(diags, summary, err)
	}
}

func (r *portsResource) put(ctx context.Context, m *portsModel, diags *diag.Diagnostics) bool {
	app, env := m.App.ValueString(), m.Environment.ValueString()
	v, err := r.apply(ctx, app, env, r.specs(ctx, m))
	if err != nil {
		portsError(diags, "No se pudieron guardar los puertos", err)
		return false
	}
	r.fill(m, app, env, v)
	return true
}

func (r *portsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan portsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *portsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan portsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *portsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st portsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, env := st.App.ValueString(), st.Environment.ValueString()
	v, err := r.c.GetPorts(ctx, app, env)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		portsError(&resp.Diagnostics, "No se pudieron leer los puertos", err)
		return
	}
	r.fill(&st, app, env, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Delete cierra TODOS los puertos del entorno (el recurso es autoritativo).
func (r *portsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st portsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.apply(ctx, st.App.ValueString(), st.Environment.ValueString(), []client.PortSpec{})
	if err != nil && !client.IsNotFound(err) {
		portsError(&resp.Diagnostics, "No se pudieron cerrar los puertos", err)
	}
}

func (r *portsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/entorno", &resp.Diagnostics)
	if p == nil {
		return
	}
	v, err := r.c.GetPorts(ctx, p[0], p[1])
	if err != nil {
		portsError(&resp.Diagnostics, "No se pudieron importar los puertos", err)
		return
	}
	if len(v.Ports) == 0 {
		resp.Diagnostics.AddError("No hay puertos abiertos", fmt.Sprintf("El entorno %s/%s no tiene puertos públicos: declara el recurso y aplica en vez de importarlo.", p[0], p[1]))
		return
	}
	var m portsModel
	r.fill(&m, p[0], p[1], v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
