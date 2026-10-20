package provider

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

func boolRequiresReplace() planmodifier.Bool { return boolplanmodifier.RequiresReplace() }

func rrString() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplace()}
}

func idAttr() schema.StringAttribute {
	return schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
}

// ================= acceso entre Apps =================

var (
	_ resource.Resource                = &accessResource{}
	_ resource.ResourceWithConfigure   = &accessResource{}
	_ resource.ResourceWithImportState = &accessResource{}
)

func NewAccessResource() resource.Resource { return &accessResource{} }

type accessResource struct{ base }

type accessModel struct {
	ID          types.String `tfsdk:"id"`
	App         types.String `tfsdk:"app"`
	Environment types.String `tfsdk:"environment"`
	From        types.String `tfsdk:"from"`
	Service     types.String `tfsdk:"service"`
	Port        types.Int64  `tfsdk:"port"`
}

func (r *accessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_access"
}

func (r *accessResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Permite que la App `from` llame al servicio `service:port` de ESTA App en el mismo entorno (solo administradores). Entre entornos del mismo nombre; dentro de una App ya se hablan.",
		Attributes: map[string]schema.Attribute{
			"id":          idAttr(),
			"app":         schema.StringAttribute{Required: true, Description: "App destino (dueña del servicio).", Validators: appNameValidators(), PlanModifiers: rrString()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rrString()},
			"from":        schema.StringAttribute{Required: true, Description: "App origen autorizada.", Validators: appNameValidators(), PlanModifiers: rrString()},
			"service":     schema.StringAttribute{Required: true, Description: "Nombre del Service de Kubernetes destino (sin comodines).", PlanModifiers: rrString()},
			"port":        schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}, PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}},
		},
	}
}

func (m *accessModel) key() string {
	return m.App.ValueString() + "/" + m.Environment.ValueString() + "/" + m.From.ValueString() + "/" + m.Service.ValueString() + "/" + strconv.FormatInt(m.Port.ValueInt64(), 10)
}

func (r *accessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan accessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.AllowAccess(ctx, plan.App.ValueString(), plan.Environment.ValueString(), client.AllowAccessReq{From: plan.From.ValueString(), Service: plan.Service.ValueString(), Port: plan.Port.ValueInt64()})
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo permitir el acceso", err)
		return
	}
	plan.ID = types.StringValue(plan.key())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *accessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st accessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.ListAccess(ctx, st.App.ValueString(), st.Environment.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el acceso", err)
		return
	}
	for _, e := range a.Incoming {
		if e.App == st.From.ValueString() && e.Service == st.Service.ValueString() && e.Port == st.Port.ValueInt64() {
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *accessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("El acceso no se actualiza en sitio", "Todos sus atributos fuerzan reemplazo.")
}

func (r *accessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st accessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RevokeAccess(ctx, st.App.ValueString(), st.Environment.ValueString(), st.From.ValueString(), st.Service.ValueString(), st.Port.ValueInt64()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo revocar el acceso", err)
	}
}

func (r *accessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 5, "app/entorno/origen/servicio/puerto", &resp.Diagnostics)
	if p == nil {
		return
	}
	port, err := strconv.ParseInt(p[4], 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Puerto no válido", fmt.Sprintf("%q no es un número.", p[4]))
		return
	}
	a, gerr := r.c.ListAccess(ctx, p[0], p[1])
	if gerr != nil {
		apiError(&resp.Diagnostics, "No se pudo importar el acceso", gerr)
		return
	}
	for _, e := range a.Incoming {
		if e.App == p[2] && e.Service == p[3] && e.Port == port {
			resp.Diagnostics.Append(resp.State.Set(ctx, &accessModel{ID: types.StringValue(req.ID), App: types.StringValue(p[0]), Environment: types.StringValue(p[1]),
				From: types.StringValue(p[2]), Service: types.StringValue(p[3]), Port: types.Int64Value(port)})...)
			return
		}
	}
	resp.Diagnostics.AddError("No se encontró la regla de acceso", req.ID)
}

// ================= escalado =================

var (
	_ resource.Resource                = &scalingResource{}
	_ resource.ResourceWithConfigure   = &scalingResource{}
	_ resource.ResourceWithImportState = &scalingResource{}
)

func NewScalingResource() resource.Resource { return &scalingResource{} }

type scalingResource struct{ base }

type horizontalModel struct {
	Mode         types.String `tfsdk:"mode"`
	Replicas     types.Int64  `tfsdk:"replicas"`
	Min          types.Int64  `tfsdk:"min"`
	Max          types.Int64  `tfsdk:"max"`
	CPUTarget    types.Int64  `tfsdk:"cpu_target"`
	MemoryTarget types.Int64  `tfsdk:"memory_target"`
}

type verticalModel struct {
	Mode           types.String `tfsdk:"mode"`
	MinCPUMilli    types.Int64  `tfsdk:"min_cpu_m"`
	MaxCPUMilli    types.Int64  `tfsdk:"max_cpu_m"`
	MinMemoryBytes types.Int64  `tfsdk:"min_memory_bytes"`
	MaxMemoryBytes types.Int64  `tfsdk:"max_memory_bytes"`
}

type scalingModel struct {
	ID          types.String     `tfsdk:"id"`
	App         types.String     `tfsdk:"app"`
	Environment types.String     `tfsdk:"environment"`
	Kind        types.String     `tfsdk:"kind"`
	Workload    types.String     `tfsdk:"workload"`
	Horizontal  *horizontalModel `tfsdk:"horizontal"`
	Vertical    *verticalModel   `tfsdk:"vertical"`
}

func (r *scalingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scaling"
}

func (r *scalingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	zero := int64default.StaticInt64(0)
	i64 := func(desc string, max int64) schema.Int64Attribute {
		return schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Description: desc, Validators: []validator.Int64{int64validator.Between(0, max)}}
	}
	resp.Schema = schema.Schema{
		Description: "Escalado de una carga (Deployment o StatefulSet) de un entorno: réplicas fijas o autoescalado horizontal, y ajuste vertical. **La carga debe existir ya** (la crea tu despliegue); si no, la API responde 404. " +
			"La API no tiene «quitar escalado»: destruir el recurso solo deja de gestionarlo.",
		Attributes: map[string]schema.Attribute{
			"id":          idAttr(),
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rrString()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rrString()},
			"kind":        schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("Deployment"), Validators: []validator.String{stringvalidator.OneOf("Deployment", "StatefulSet")}, PlanModifiers: rrString()},
			"workload":    schema.StringAttribute{Required: true, Description: "Nombre de la carga.", PlanModifiers: rrString()},
			"horizontal": schema.SingleNestedAttribute{Optional: true, Description: "Escalado horizontal (no aplica a StatefulSet).", Attributes: map[string]schema.Attribute{
				"mode":          schema.StringAttribute{Required: true, Description: "`fixed` (réplicas fijas) o `auto` (autoescalado).", Validators: []validator.String{stringvalidator.OneOf("fixed", "auto")}},
				"replicas":      i64("Réplicas fijas (1 a 50) con mode = fixed.", 50),
				"min":           i64("Mínimo de réplicas con mode = auto.", 50),
				"max":           i64("Máximo de réplicas con mode = auto.", 50),
				"cpu_target":    i64("Umbral de CPU en % con mode = auto.", 100),
				"memory_target": i64("Umbral de memoria en % con mode = auto.", 100),
			}},
			"vertical": schema.SingleNestedAttribute{Optional: true, Description: "Ajuste vertical (VPA; requiere que el cluster lo tenga).", Attributes: map[string]schema.Attribute{
				"mode":             schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("off", "recommend", "auto")}},
				"min_cpu_m":        i64("Mínimo de CPU en milinúcleos.", 1<<40),
				"max_cpu_m":        i64("Máximo de CPU en milinúcleos.", 1<<40),
				"min_memory_bytes": i64("Mínimo de memoria en bytes.", 1<<50),
				"max_memory_bytes": i64("Máximo de memoria en bytes.", 1<<50),
			}},
		},
	}
}

func (r *scalingResource) apply(ctx context.Context, m *scalingModel) error {
	var rq client.SetScalingReq
	if h := m.Horizontal; h != nil {
		rq.Horizontal = &client.ScaleHorizontalReq{Mode: h.Mode.ValueString(), Replicas: h.Replicas.ValueInt64(), Min: h.Min.ValueInt64(), Max: h.Max.ValueInt64(),
			CPUTarget: h.CPUTarget.ValueInt64(), MemoryTarget: h.MemoryTarget.ValueInt64()}
	}
	if v := m.Vertical; v != nil {
		rq.Vertical = &client.ScaleVerticalReq{Mode: v.Mode.ValueString(), MinCPUMilli: v.MinCPUMilli.ValueInt64(), MaxCPUMilli: v.MaxCPUMilli.ValueInt64(),
			MinMemoryBytes: v.MinMemoryBytes.ValueInt64(), MaxMemoryBytes: v.MaxMemoryBytes.ValueInt64()}
	}
	m.ID = types.StringValue(m.App.ValueString() + "/" + m.Environment.ValueString() + "/" + m.Kind.ValueString() + "/" + m.Workload.ValueString())
	return r.c.SetScaling(ctx, m.App.ValueString(), m.Environment.ValueString(), m.Kind.ValueString(), m.Workload.ValueString(), rq)
}

func (r *scalingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scalingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Horizontal == nil && plan.Vertical == nil {
		resp.Diagnostics.AddError("No hay nada que escalar", "Indica `horizontal` y/o `vertical`.")
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		if client.IsNotFound(err) {
			err = fmt.Errorf("%w (la carga debe existir antes: despliégala primero y declara la dependencia)", err)
		}
		apiError(&resp.Diagnostics, "No se pudo aplicar el escalado", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *scalingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan scalingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		apiError(&resp.Diagnostics, "No se pudo aplicar el escalado", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *scalingResource) lookup(ctx context.Context, app, env, kind, name string) (client.ScaleWorkload, bool, error) {
	s, err := r.c.GetScaling(ctx, app, env)
	if err != nil {
		return client.ScaleWorkload{}, false, err
	}
	for _, w := range s.Workloads {
		if w.Kind == kind && w.Name == name {
			return w, true, nil
		}
	}
	return client.ScaleWorkload{}, false, nil
}

func (r *scalingResource) fill(m *scalingModel, w client.ScaleWorkload, all bool) {
	if m.Horizontal != nil || all {
		h := &horizontalModel{Mode: types.StringValue("fixed"), Replicas: types.Int64Value(w.Replicas), Min: types.Int64Value(0), Max: types.Int64Value(0), CPUTarget: types.Int64Value(0), MemoryTarget: types.Int64Value(0)}
		if w.HPA != nil && w.HPA.Managed {
			h = &horizontalModel{Mode: types.StringValue("auto"), Replicas: types.Int64Value(0), Min: types.Int64Value(w.HPA.Min), Max: types.Int64Value(w.HPA.Max),
				CPUTarget: types.Int64Value(w.HPA.CPUTarget), MemoryTarget: types.Int64Value(w.HPA.MemoryTarget)}
		}
		m.Horizontal = h
	}
	if m.Vertical != nil || (all && w.Vertical != nil) {
		v := &verticalModel{Mode: types.StringValue("off"), MinCPUMilli: types.Int64Value(0), MaxCPUMilli: types.Int64Value(0), MinMemoryBytes: types.Int64Value(0), MaxMemoryBytes: types.Int64Value(0)}
		if w.Vertical != nil {
			v = &verticalModel{Mode: types.StringValue(w.Vertical.Mode), MinCPUMilli: types.Int64Value(w.Vertical.Min.CPUMilli), MaxCPUMilli: types.Int64Value(w.Vertical.Max.CPUMilli),
				MinMemoryBytes: types.Int64Value(w.Vertical.Min.MemoryBytes), MaxMemoryBytes: types.Int64Value(w.Vertical.Max.MemoryBytes)}
		}
		m.Vertical = v
	}
}

func (r *scalingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st scalingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w, found, err := r.lookup(ctx, st.App.ValueString(), st.Environment.ValueString(), st.Kind.ValueString(), st.Workload.ValueString())
	if client.IsNotFound(err) || (err == nil && !found) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el escalado", err)
		return
	}
	r.fill(&st, w, false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *scalingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("El escalado no se deshace", "La API no tiene «quitar escalado»: la carga conserva su configuración actual y solo se deja de gestionar con Terraform.")
}

func (r *scalingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 4, "app/entorno/tipo/carga", &resp.Diagnostics)
	if p == nil {
		return
	}
	w, found, err := r.lookup(ctx, p[0], p[1], p[2], p[3])
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("el entorno %s/%s no tiene la carga %s/%s", p[0], p[1], p[2], p[3])
		}
		apiError(&resp.Diagnostics, "No se pudo importar el escalado", err)
		return
	}
	m := scalingModel{ID: types.StringValue(req.ID), App: types.StringValue(p[0]), Environment: types.StringValue(p[1]), Kind: types.StringValue(p[2]), Workload: types.StringValue(p[3])}
	r.fill(&m, w, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= cola =================

var (
	_ resource.Resource                = &queueResource{}
	_ resource.ResourceWithConfigure   = &queueResource{}
	_ resource.ResourceWithImportState = &queueResource{}
)

func NewQueueResource() resource.Resource { return &queueResource{} }

type queueResource struct{ base }

type jetstreamModel struct {
	Subjects  types.Set    `tfsdk:"subjects"`
	Retention types.String `tfsdk:"retention"`
	Replicas  types.Int64  `tfsdk:"replicas"`
	Storage   types.String `tfsdk:"storage"`
	MaxAge    types.Int64  `tfsdk:"max_age_seconds"`
	MaxMsgs   types.Int64  `tfsdk:"max_msgs"`
	MaxBytes  types.Int64  `tfsdk:"max_bytes"`
}

type queueModel struct {
	ID              types.String    `tfsdk:"id"`
	Kind            types.String    `tfsdk:"kind"`
	Name            types.String    `tfsdk:"name"`
	Display         types.String    `tfsdk:"display"`
	Hidden          types.Bool      `tfsdk:"hidden"`
	JetStream       *jetstreamModel `tfsdk:"jetstream"`
	ConfirmDataLoss types.Bool      `tfsdk:"confirm_data_loss"`
}

func (r *queueResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_queue"
}

func (r *queueResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	zero := int64default.StaticInt64(0)
	jsRR := func() []planmodifier.Int64 { return []planmodifier.Int64{int64planmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Una cola declarada en Bifröst (solo administradores). `bullmq` se registra; `jetstream` crea el stream en NATS. Solo `display` y `hidden` se editan: el resto cambia reemplazando la cola. " +
			"Quitar una cola JetStream **borra el stream, su DLQ y sus consumidores**: exige `confirm_data_loss = true` ya aplicado.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"kind":    schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("bullmq"), Validators: []validator.String{stringvalidator.OneOf("bullmq", "jetstream")}, PlanModifiers: rrString()},
			"name":    schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(queueNameRe, "debe empezar con letra o número y usar solo letras, números, «_», «.» o «-» (máx 80; sin «:»)")}, PlanModifiers: rrString()},
			"display": schema.StringAttribute{Optional: true, Computed: true, Description: "Nombre visible (hasta 60). Por omisión, el nombre.", Validators: []validator.String{stringvalidator.LengthAtMost(60)}, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"hidden":  schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Ocultar la cola del tablero."},
			"jetstream": schema.SingleNestedAttribute{Optional: true, Description: "Parámetros del stream (solo con kind = jetstream). Inmutables.", PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
				Attributes: map[string]schema.Attribute{
					"subjects":        schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Subjects (máx 16)."},
					"retention":       schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("workqueue", "limits")}},
					"replicas":        schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.OneOf(1, 3)}},
					"storage":         schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("file", "memory")}},
					"max_age_seconds": schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Description: "Edad máxima de los mensajes; 0 = sin límite.", Validators: []validator.Int64{int64validator.Between(0, 3650*86400)}},
					"max_msgs":        schema.Int64Attribute{Optional: true, Computed: true, Default: zero},
					"max_bytes":       schema.Int64Attribute{Optional: true, Computed: true, Default: zero},
				}},
			"confirm_data_loss": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Protección para quitar colas JetStream (se borran los mensajes)."},
		},
	}
	_ = jsRR
}

func (r *queueResource) find(ctx context.Context, kind, name string) (client.Queue, bool, error) {
	l, err := r.c.ListQueues(ctx)
	if err != nil {
		return client.Queue{}, false, err
	}
	for _, q := range l {
		if q.Kind == kind && q.Name == name {
			return q, true, nil
		}
	}
	return client.Queue{}, false, nil
}

func (r *queueResource) fill(m *queueModel, q client.Queue) {
	m.ID = types.StringValue(q.Kind + "/" + q.Name)
	m.Kind, m.Name, m.Display, m.Hidden = types.StringValue(q.Kind), types.StringValue(q.Name), types.StringValue(q.Display), types.BoolValue(q.Hidden)
	if j := q.JetStream; j != nil {
		m.JetStream = &jetstreamModel{Subjects: strSet(j.Subjects), Retention: types.StringValue(j.Retention), Replicas: types.Int64Value(j.Replicas), Storage: types.StringValue(j.Storage),
			MaxAge: types.Int64Value(j.MaxAge), MaxMsgs: types.Int64Value(j.MaxMsgs), MaxBytes: types.Int64Value(j.MaxBytes)}
	} else {
		m.JetStream = nil
	}
}

func (r *queueResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan queueModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	kind := plan.Kind.ValueString()
	if (kind == "jetstream") != (plan.JetStream != nil) {
		resp.Diagnostics.AddError("Configuración de cola incoherente", "`jetstream` es obligatorio con kind = jetstream y no se admite con bullmq.")
		return
	}
	rq := client.CreateQueueReq{Name: plan.Name.ValueString(), Display: str(plan.Display), Kind: kind}
	if j := plan.JetStream; j != nil {
		rq.JetStream = &client.JetStreamSpec{Subjects: setStrings(ctx, j.Subjects), Retention: j.Retention.ValueString(), Replicas: j.Replicas.ValueInt64(), Storage: j.Storage.ValueString(),
			MaxAge: j.MaxAge.ValueInt64(), MaxMsgs: j.MaxMsgs.ValueInt64(), MaxBytes: j.MaxBytes.ValueInt64()}
	}
	q, err := r.c.CreateQueue(ctx, rq)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo crear la cola", err)
		return
	}
	if plan.Hidden.ValueBool() != q.Hidden {
		h := plan.Hidden.ValueBool()
		if q, err = r.c.UpdateQueue(ctx, kind, q.Name, client.UpdateQueueReq{Hidden: &h}); err != nil {
			apiError(&resp.Diagnostics, "La cola se creó pero no se pudo ocultar", err)
			return
		}
	}
	jsPlan := plan.JetStream
	r.fill(&plan, q)
	if jsPlan != nil && plan.JetStream != nil { // el plan manda: la API puede normalizar subjects
		plan.JetStream = jsPlan
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *queueResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st queueModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	q, found, err := r.find(ctx, st.Kind.ValueString(), st.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la cola", err)
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	r.fill(&st, q)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *queueResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st queueModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rq := client.UpdateQueueReq{}
	if d := str(plan.Display); d != "" && d != st.Display.ValueString() {
		rq.Display = &d
	}
	if h := plan.Hidden.ValueBool(); h != st.Hidden.ValueBool() {
		rq.Hidden = &h
	}
	if rq.Display != nil || rq.Hidden != nil {
		q, err := r.c.UpdateQueue(ctx, st.Kind.ValueString(), st.Name.ValueString(), rq)
		if err != nil {
			apiError(&resp.Diagnostics, "No se pudo actualizar la cola", err)
			return
		}
		js := plan.JetStream
		r.fill(&plan, q)
		plan.JetStream = js
	} else {
		plan.ID, plan.Display = st.ID, st.Display
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *queueResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st queueModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if st.Kind.ValueString() == "jetstream" && !st.ConfirmDataLoss.ValueBool() {
		resp.Diagnostics.AddError("Protección contra pérdida de datos",
			fmt.Sprintf("Quitar la cola JetStream %q borra el stream, su DLQ y sus consumidores. Pon `confirm_data_loss = true`, ejecuta `terraform apply` y repite la operación.", st.Name.ValueString()))
		return
	}
	if err := r.c.RemoveQueue(ctx, st.Kind.ValueString(), st.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo quitar la cola", err)
	}
}

func (r *queueResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "tipo/nombre (bullmq|jetstream)", &resp.Diagnostics)
	if p == nil {
		return
	}
	q, found, err := r.find(ctx, p[0], p[1])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la cola", err)
		return
	}
	if !found {
		resp.Diagnostics.AddError("No se encontró la cola", req.ID)
		return
	}
	m := queueModel{ConfirmDataLoss: types.BoolValue(false)}
	r.fill(&m, q)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= enlace de replicación =================

var (
	_ resource.Resource              = &replicationLinkResource{}
	_ resource.ResourceWithConfigure = &replicationLinkResource{}
)

func NewReplicationLinkResource() resource.Resource { return &replicationLinkResource{} }

type replicationLinkResource struct{ base }

type replicationLinkModel struct {
	ID              types.String   `tfsdk:"id"`
	Name            types.String   `tfsdk:"name"`
	Source          types.String   `tfsdk:"source"`
	Destination     types.String   `tfsdk:"destination"`
	Mode            types.String   `tfsdk:"mode"`
	App             types.String   `tfsdk:"app"`
	WaitForApproval types.Bool     `tfsdk:"wait_for_approval"`
	Approved        types.Bool     `tfsdk:"approved"`
	State           types.String   `tfsdk:"state"`
	NativeURL       types.String   `tfsdk:"native_url"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *replicationLinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_replication_link"
}

func (r *replicationLinkResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Propuesta de un enlace de replicación (Ratatoskr). **Solo propone**: el enlace queda pendiente de aprobación en Ratatoskr (`approved = false`); las conexiones deben existir allí. " +
			"La API no permite editar ni borrar enlaces: cambiar cualquier atributo propone otro enlace (reemplazo) y destruir solo lo quita del state (brecha de API documentada).",
		Attributes: map[string]schema.Attribute{
			"id":                idAttr(),
			"name":              schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(poolNameRe, "debe empezar con letra minúscula o número y usar solo minúsculas, números y «-» (máx 40)")}, PlanModifiers: rrString()},
			"source":            schema.StringAttribute{Required: true, Description: "Conexión de origen (existente en Ratatoskr).", PlanModifiers: rrString()},
			"destination":       schema.StringAttribute{Required: true, Description: "Conexión de destino.", PlanModifiers: rrString()},
			"mode":              schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("one_way"), Validators: []validator.String{stringvalidator.OneOf("one_way", "two_way")}, PlanModifiers: rrString()},
			"app":               schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "App asociada (opcional).", PlanModifiers: rrString()},
			"wait_for_approval": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Esperar (hasta el timeout de creación) a que Ratatoskr apruebe el enlace."},
			"approved":          schema.BoolAttribute{Computed: true, Description: "El enlace ya existe en Ratatoskr."},
			"state":             schema.StringAttribute{Computed: true, Description: "Estado de la propuesta o del enlace."},
			"native_url":        schema.StringAttribute{Computed: true, Description: "Dónde aprobar la propuesta."},
			"timeouts":          timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *replicationLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan replicationLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := plan.Timeouts.Create(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	name := plan.Name.ValueString()
	p, err := r.c.ProposeLink(ctx, client.ProposeLinkReq{Name: name, Source: plan.Source.ValueString(), Destination: plan.Destination.ValueString(), Mode: plan.Mode.ValueString(), App: plan.App.ValueString()})
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo proponer el enlace", err)
		return
	}
	plan.ID, plan.State, plan.NativeURL, plan.Approved = types.StringValue(name), types.StringValue(p.State), types.StringValue(p.NativeURL), types.BoolValue(false)
	if plan.WaitForApproval.ValueBool() {
	wait:
		for {
			l, gerr := r.c.GetLink(ctx, name)
			switch {
			case gerr == nil:
				plan.Approved, plan.State = types.BoolValue(true), types.StringValue(l.Status)
				break wait
			case ctx.Err() != nil: // se agotó el tiempo en medio de la consulta: es el aviso de «sigue pendiente», no un fallo
				resp.Diagnostics.AddWarning("El enlace sigue pendiente de aprobación", "Se agotó el tiempo esperando la aprobación en Ratatoskr ("+plan.NativeURL.ValueString()+"). El recurso queda registrado como propuesto.")
				break wait
			case !client.IsNotFound(gerr):
				apiError(&resp.Diagnostics, "No se pudo consultar el enlace", gerr)
				break wait
			}
			select {
			case <-ctx.Done():
				resp.Diagnostics.AddWarning("El enlace sigue pendiente de aprobación", "Se agotó el tiempo esperando la aprobación en Ratatoskr ("+plan.NativeURL.ValueString()+"). El recurso queda registrado como propuesto.")
				break wait
			case <-time.After(pollInterval):
			}
		}
	}
	ctx = context.Background()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if !plan.Approved.ValueBool() {
		resp.Diagnostics.AddWarning("Enlace pendiente de aprobación", "Apruébalo en Ratatoskr: "+plan.NativeURL.ValueString())
	}
}

func (r *replicationLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st replicationLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.c.GetLink(ctx, st.Name.ValueString())
	switch {
	case err == nil:
		st.Approved, st.State = types.BoolValue(true), types.StringValue(l.Status)
	case client.IsNotFound(err):
		if st.Approved.ValueBool() { // existía y desapareció: borrado externo
			resp.State.RemoveResource(ctx)
			return
		}
	default:
		apiError(&resp.Diagnostics, "No se pudo leer el enlace", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *replicationLinkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st replicationLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.Approved, plan.State, plan.NativeURL = st.ID, st.Approved, st.State, st.NativeURL
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *replicationLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning("El enlace no se borra en Ratatoskr", "La API de Bifröst no permite borrar enlaces de replicación: se quita del state y se debe eliminar desde Ratatoskr.")
}

// ================= pool de runners =================

var (
	_ resource.Resource                = &runnerPoolResource{}
	_ resource.ResourceWithConfigure   = &runnerPoolResource{}
	_ resource.ResourceWithImportState = &runnerPoolResource{}
)

func NewRunnerPoolResource() resource.Resource { return &runnerPoolResource{} }

type runnerPoolResource struct{ base }

type runnerPoolModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Labels  types.Set    `tfsdk:"labels"`
	Scope   types.String `tfsdk:"scope"`
	Min     types.Int64  `tfsdk:"min"`
	Max     types.Int64  `tfsdk:"max"`
	Origins types.Set    `tfsdk:"origins"`
	Paused  types.Bool   `tfsdk:"paused"`
	App     types.String `tfsdk:"app"`
}

func (r *runnerPoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_runner_pool"
}

func (r *runnerPoolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Pool de runners de CI (solo administradores). `name`, `labels` y `scope` no se pueden cambiar (reemplazan el pool). Los límites, los orígenes, la pausa y la App se editan en sitio.",
		Attributes: map[string]schema.Attribute{
			"id":      idAttr(),
			"name":    schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.RegexMatches(poolNameRe, "debe empezar con letra minúscula o número y usar solo minúsculas, números y «-» (máx 40)")}, PlanModifiers: rrString()},
			"labels":  schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: emptySet, Description: "Etiquetas (máx 10).", PlanModifiers: []planmodifier.Set{setplanmodifier.RequiresReplace()}},
			"scope":   schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("project"), Validators: []validator.String{stringvalidator.OneOf("project", "group", "shared")}, PlanModifiers: rrString()},
			"min":     schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), Validators: []validator.Int64{int64validator.Between(0, 100)}},
			"max":     schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(4), Validators: []validator.Int64{int64validator.Between(1, 100)}},
			"origins": schema.SetAttribute{Required: true, ElementType: types.StringType, Description: "Orígenes de máquinas (`own`, `aws`, `gcloud` u otro que reporte el orquestador). Al menos uno.", Validators: nil},
			"paused":  schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"app":     schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "App que construye este pool (vacío = ninguna)."},
		},
	}
}

func (r *runnerPoolResource) fill(m *runnerPoolModel, p client.RunnerPool) {
	m.ID, m.Name = types.StringValue(p.Name), types.StringValue(p.Name)
	m.Labels, m.Scope, m.Min, m.Max, m.Origins = strSet(p.Labels), types.StringValue(p.Scope), types.Int64Value(p.Min), types.Int64Value(p.Max), strSet(p.Origins)
	m.Paused, m.App = types.BoolValue(p.Paused), types.StringValue(p.App)
}

func (r *runnerPoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan runnerPoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(plan.Origins.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(rootAttr("origins"), "Faltan orígenes", "Elige al menos un origen de máquinas.")
		return
	}
	mn, mx := plan.Min.ValueInt64(), plan.Max.ValueInt64()
	p, err := r.c.CreateRunnerPool(ctx, client.CreateRunnerPoolReq{Name: plan.Name.ValueString(), Labels: setStrings(ctx, plan.Labels), Scope: plan.Scope.ValueString(),
		Min: &mn, Max: &mx, Origins: setStrings(ctx, plan.Origins), App: plan.App.ValueString()})
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo crear el pool de runners", err)
		return
	}
	paused := plan.Paused.ValueBool()
	if paused != p.Paused {
		if p, err = r.c.UpdateRunnerPool(ctx, p.Name, client.UpdateRunnerPoolReq{Paused: &paused}); err != nil {
			apiError(&resp.Diagnostics, "El pool se creó pero no se pudo pausar", err)
			return
		}
	}
	r.fill(&plan, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *runnerPoolResource) find(ctx context.Context, name string) (client.RunnerPool, bool, error) {
	l, err := r.c.ListRunnerPools(ctx)
	if err != nil {
		return client.RunnerPool{}, false, err
	}
	for _, p := range l {
		if p.Name == name {
			return p, true, nil
		}
	}
	return client.RunnerPool{}, false, nil
}

func (r *runnerPoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st runnerPoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, found, err := r.find(ctx, st.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el pool", err)
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	r.fill(&st, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *runnerPoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan runnerPoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	paused, app, mn, mx := plan.Paused.ValueBool(), plan.App.ValueString(), plan.Min.ValueInt64(), plan.Max.ValueInt64()
	origins := setStrings(ctx, plan.Origins)
	p, err := r.c.UpdateRunnerPool(ctx, plan.Name.ValueString(), client.UpdateRunnerPoolReq{Paused: &paused, App: &app, Origins: &origins, Min: &mn, Max: &mx})
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo actualizar el pool", err)
		return
	}
	r.fill(&plan, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *runnerPoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st runnerPoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RemoveRunnerPool(ctx, st.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo quitar el pool", err)
	}
}

func (r *runnerPoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p, found, err := r.find(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar el pool", err)
		return
	}
	if !found {
		resp.Diagnostics.AddError("No se encontró el pool", req.ID)
		return
	}
	var m runnerPoolModel
	r.fill(&m, p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= administrador =================

var (
	_ resource.Resource                = &adminResource{}
	_ resource.ResourceWithConfigure   = &adminResource{}
	_ resource.ResourceWithImportState = &adminResource{}
)

func NewAdminResource() resource.Resource { return &adminResource{} }

type adminResource struct{ base }

type adminModel struct {
	ID     types.String `tfsdk:"id"`
	Email  types.String `tfsdk:"email"`
	Source types.String `tfsdk:"source"`
}

func (r *adminResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_admin"
}

func (r *adminResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Una persona administradora de la plataforma (solo administradores). Los administradores de arranque (`source = bootstrap`) se definen en la configuración de la plataforma y no se pueden quitar desde aquí; la plataforma nunca se queda sin administradores.",
		Attributes: map[string]schema.Attribute{
			"id":     idAttr(),
			"email":  schema.StringAttribute{Required: true, Validators: emailValidators(), PlanModifiers: rrString()},
			"source": schema.StringAttribute{Computed: true, Description: "`bifrost` (añadido desde Bifröst) o `bootstrap`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *adminResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan adminModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.AddAdmin(ctx, plan.Email.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo añadir al administrador", err)
		return
	}
	plan.ID, plan.Source = types.StringValue(a.Email), types.StringValue(a.Source)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *adminResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st adminModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.c.ListAdmins(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la lista de administradores", err)
		return
	}
	for _, a := range l {
		if a.Email == st.Email.ValueString() {
			st.Source = types.StringValue(a.Source)
			resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *adminResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("El administrador no se actualiza en sitio", "El correo fuerza reemplazo.")
}

func (r *adminResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st adminModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.RemoveAdmin(ctx, st.Email.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo quitar al administrador", err)
	}
}

func (r *adminResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	l, err := r.c.ListAdmins(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar al administrador", err)
		return
	}
	for _, a := range l {
		if a.Email == req.ID {
			resp.Diagnostics.Append(resp.State.Set(ctx, &adminModel{ID: types.StringValue(a.Email), Email: types.StringValue(a.Email), Source: types.StringValue(a.Source)})...)
			return
		}
	}
	resp.Diagnostics.AddError("No se encontró al administrador", req.ID)
}
