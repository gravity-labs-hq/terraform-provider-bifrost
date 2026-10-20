package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

var (
	_ resource.Resource                = &serviceResource{}
	_ resource.ResourceWithConfigure   = &serviceResource{}
	_ resource.ResourceWithImportState = &serviceResource{}
)

func NewServiceResource() resource.Resource { return &serviceResource{} }

type serviceResource struct{ base }

type serviceModel struct {
	ID              types.String   `tfsdk:"id"`
	App             types.String   `tfsdk:"app"`
	Environment     types.String   `tfsdk:"environment"`
	Service         types.String   `tfsdk:"service"`
	Options         types.Map      `tfsdk:"options"`
	Dedicated       types.Bool     `tfsdk:"dedicated"`
	ConfirmDataLoss types.Bool     `tfsdk:"confirm_data_loss"`
	SecretName      types.String   `tfsdk:"secret_name"`
	Resources       types.List     `tfsdk:"resources"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (r *serviceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	resp.Schema = schema.Schema{
		Description: "Una herramienta del catálogo (postgres, redis, ...) añadida a un entorno. **Destruirla o reemplazarla borra los datos del entorno en ella**: la API lo exige con `confirm`; " +
			"este recurso solo lo envía si `confirm_data_loss = true` ya está aplicado. Las opciones no se pueden cambiar sin recrear la herramienta, y la API no las devuelve al leer (no hay detección de drift de `options`).",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rr()},
			"service":     schema.StringAttribute{Required: true, Description: "Id de la herramienta en el catálogo (`postgres`, `redis`...).", PlanModifiers: rr()},
			"options": schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: "Opciones de la herramienta (ver `GET /services/{id}`). Inmutables.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()}},
			"dedicated": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Instancia dedicada (si la herramienta lo admite).",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()}},
			"confirm_data_loss": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Protección: debe estar en `true` (y aplicado) para poder destruir o reemplazar la herramienta, porque se borran los datos del entorno en ella."},
			"secret_name": schema.StringAttribute{Computed: true, Description: "Secret de Kubernetes del entorno con los datos de conexión (los valores sensibles viven solo ahí).", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"resources":   schema.ListAttribute{Computed: true, ElementType: types.StringType, PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}},
			"timeouts":    timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *serviceResource) fill(m *serviceModel, s client.Service) {
	m.ID = types.StringValue(m.App.ValueString() + "/" + m.Environment.ValueString() + "/" + s.Service)
	m.Service = types.StringValue(s.Service)
	m.Dedicated = types.BoolValue(s.Dedicated)
	m.SecretName = types.StringValue(s.SecretName)
	m.Resources = strList(s.Resources)
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := plan.Timeouts.Create(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	app, env, id := plan.App.ValueString(), plan.Environment.ValueString(), plan.Service.ValueString()
	// El entorno debe estar activo para añadir una herramienta.
	if _, err := waitEnvActive(ctx, r.c, app, env, to); err != nil {
		apiError(&resp.Diagnostics, "El entorno no está activo", err)
		return
	}
	sv, err := r.c.AttachService(ctx, app, env, client.AttachServiceReq{Service: id, Options: mapStrings(ctx, plan.Options), Dedicated: plan.Dedicated.ValueBool()})
	var warn error
	if err != nil {
		if !isStatus(err, 502) {
			apiError(&resp.Diagnostics, "No se pudo añadir la herramienta", err)
			return
		}
		// 502: la herramienta quedó añadida pero no se abrió la red; se reconcilia el entorno.
		e, gerr := r.c.GetEnv(ctx, app, env)
		found := false
		for _, s := range e.Services {
			if s.Service == id {
				sv, found = s, true
			}
		}
		if gerr != nil || !found {
			apiError(&resp.Diagnostics, "No se pudo añadir la herramienta", err)
			return
		}
		warn = err
		if _, rerr := r.c.ReconcileEnv(ctx, app, env); rerr != nil {
			warn = fmt.Errorf("%w; además falló reconciliar: %v", err, rerr)
		} else {
			warn = nil
		}
	}
	r.fill(&plan, sv)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if warn != nil {
		resp.Diagnostics.AddWarning("La herramienta se añadió pero no se pudo abrir su red", warn.Error())
	}
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.c.GetEnv(ctx, st.App.ValueString(), st.Environment.ValueString())
	if client.IsNotFound(err) || (err == nil && e.Status == client.StatusArchived) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el entorno de la herramienta", err)
		return
	}
	for _, s := range e.Services {
		if s.Service == st.Service.ValueString() {
			r.fill(&st, s)
			resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// Update solo cambia el interruptor de protección (no llama a la API).
func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan serviceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st serviceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !st.ConfirmDataLoss.ValueBool() {
		resp.Diagnostics.AddError("Protección contra pérdida de datos",
			fmt.Sprintf("Quitar %q de %s/%s borra los datos del entorno en esa herramienta. Pon `confirm_data_loss = true`, ejecuta `terraform apply` y luego repite la operación.",
				st.Service.ValueString(), st.App.ValueString(), st.Environment.ValueString()))
		return
	}
	to, d := st.Timeouts.Delete(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	if err := r.c.DetachService(ctx, st.App.ValueString(), st.Environment.ValueString(), st.Service.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "No se pudo quitar la herramienta", err)
	}
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 3, "app/entorno/herramienta", &resp.Diagnostics)
	if p == nil {
		return
	}
	e, err := r.c.GetEnv(ctx, p[0], p[1])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la herramienta", err)
		return
	}
	for _, s := range e.Services {
		if s.Service == p[2] {
			m := serviceModel{App: types.StringValue(p[0]), Environment: types.StringValue(p[1]), Options: types.MapNull(types.StringType), ConfirmDataLoss: types.BoolValue(false), Timeouts: nullTimeouts()}
			r.fill(&m, s)
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			resp.Diagnostics.AddWarning("Las opciones no se pueden leer", "La API no devuelve las opciones con que se creó la herramienta: si tu configuración las declara, Terraform planeará un reemplazo (que borra datos). Usa `lifecycle { ignore_changes = [options] }`.")
			return
		}
	}
	resp.Diagnostics.AddError("No se encontró la herramienta", fmt.Sprintf("El entorno %s/%s no tiene %q.", p[0], p[1], p[2]))
}
