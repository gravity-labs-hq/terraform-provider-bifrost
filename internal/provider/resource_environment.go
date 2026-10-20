package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

var (
	_ resource.Resource                = &environmentResource{}
	_ resource.ResourceWithConfigure   = &environmentResource{}
	_ resource.ResourceWithImportState = &environmentResource{}
	_ resource.ResourceWithModifyPlan  = &environmentResource{}
)

func NewEnvironmentResource() resource.Resource { return &environmentResource{} }

type environmentResource struct{ base }

type environmentModel struct {
	ID              types.String   `tfsdk:"id"`
	App             types.String   `tfsdk:"app"`
	Name            types.String   `tfsdk:"name"`
	Quota           types.Object   `tfsdk:"quota"`
	Status          types.String   `tfsdk:"status"`
	Namespace       types.String   `tfsdk:"namespace"`
	OnDestroy       types.String   `tfsdk:"on_destroy"`
	AdoptExisting   types.Bool     `tfsdk:"adopt_existing"`
	RestoreArchived types.Bool     `tfsdk:"restore_archived"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Un entorno de una App (su propio namespace `app-<app>-<entorno>`, cuota y red cerrada). Destruir lo archiva; `on_destroy = \"purge\"` lo elimina (solo un administrador; exige retención cumplida y sin herramientas).",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":       schema.StringAttribute{Required: true, Description: "Nombre de la App.", Validators: appNameValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"name":      schema.StringAttribute{Required: true, Description: "Nombre del entorno (slug, 2 a 16).", Validators: envNameValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"quota":     quotaObjectAttr(),
			"status":    schema.StringAttribute{Computed: true, Description: "`active`, `provisioning`, `error` o `archived`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"namespace": schema.StringAttribute{Computed: true, Description: "Namespace de Kubernetes del entorno.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"on_destroy": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("archive"), Validators: []validatorString{stringvalidator.OneOf("archive", "purge")},
				Description: "`archive` (por omisión) o `purge` (eliminación definitiva e irreversible)."},
			"adopt_existing": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Si el entorno ya existe (p. ej. el que la API crea junto con la App), adoptarlo en vez de fallar con 409. Si indicas `quota`, debe coincidir con la real."},
			"restore_archived": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: "Si existe archivado, restaurarlo en vez de fallar con 409."},
			"timeouts":         timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// ModifyPlan rechaza cambiar la cuota de un entorno existente: la API no tiene esa operación (brecha #1 de docs/inventario-api.md).
func (r *environmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var st, plan environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if s := st.Status.ValueString(); s != "" && s != client.StatusActive {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, rootAttr("status"), types.StringUnknown())...) // fuerza un update que reconcilia
	}
	if plan.Quota.IsUnknown() || st.Quota.IsNull() {
		return
	}
	if !plan.Quota.Equal(st.Quota) {
		resp.Diagnostics.AddAttributeError(rootAttr("quota"), "La cuota de un entorno no se puede cambiar",
			"La API de Bifröst no ofrece cambiar la cuota de un entorno existente (`bifrost_quota` solo fija la cuota por omisión de entornos nuevos). "+
				"Para otra cuota, crea un entorno nuevo con otro nombre y migra, o quita `quota` de la configuración para conservar la actual.")
	}
}

func (r *environmentResource) fill(m *environmentModel, app string, e client.Env) {
	m.ID = types.StringValue(app + "/" + e.Name)
	m.App, m.Name = types.StringValue(app), types.StringValue(e.Name)
	m.Quota = quotaObject(e.Quota)
	m.Status, m.Namespace = types.StringValue(e.Status), types.StringValue(e.Namespace)
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := plan.Timeouts.Create(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	app, name := plan.App.ValueString(), plan.Name.ValueString()
	q, d := quotaFromObject(ctx, plan.Quota)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	var createErr error
	e, err := r.c.CreateEnv(ctx, app, client.CreateEnvReq{Name: name, Quota: q})
	switch {
	case err == nil:
	case client.IsConflict(err) && (plan.AdoptExisting.ValueBool() || plan.RestoreArchived.ValueBool()):
		ex, gerr := r.c.GetEnv(ctx, app, name)
		if gerr != nil {
			apiError(&resp.Diagnostics, "No se pudo crear el entorno", err)
			return
		}
		if ex.Status == client.StatusArchived {
			if !plan.RestoreArchived.ValueBool() {
				apiError(&resp.Diagnostics, "El entorno existe pero está archivado", fmt.Errorf("%w; usa restore_archived = true para restaurarlo", err))
				return
			}
			if ex, gerr = r.c.RestoreEnv(ctx, app, name); gerr != nil {
				if client.IsForbidden(gerr) {
					gerr = fmt.Errorf("%w. %s", gerr, restoreHint)
				}
				apiError(&resp.Diagnostics, "No se pudo restaurar el entorno archivado", gerr)
				return
			}
		} else if !plan.AdoptExisting.ValueBool() {
			apiError(&resp.Diagnostics, "No se pudo crear el entorno", err)
			return
		}
		if q != nil && !sameQuota(*q, ex.Quota) {
			resp.Diagnostics.AddError("El entorno existente tiene otra cuota",
				fmt.Sprintf("Al adoptar %s/%s la cuota pedida (%s) no coincide con la real (%s) y la API no permite cambiarla. Ajusta `quota` a la real o quítala.", app, name, quotaString(*q), quotaString(ex.Quota)))
			return
		}
		e = ex
	case isStatus(err, 502):
		var gerr error
		if e, gerr = r.c.GetEnv(ctx, app, name); gerr != nil {
			apiError(&resp.Diagnostics, "No se pudo crear el entorno", err)
			return
		}
		createErr = err
	default:
		apiError(&resp.Diagnostics, "No se pudo crear el entorno", err)
		return
	}
	if e2, werr := waitEnvActive(ctx, r.c, app, name, to); werr != nil {
		createErr = werr
	} else {
		e, createErr = e2, nil
	}
	r.fill(&plan, app, e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if createErr != nil {
		apiError(&resp.Diagnostics, "El entorno se creó pero no quedó activo (queda marcado como dañado y se reintentará)", createErr)
	}
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app := st.App.ValueString()
	e, err := r.c.GetEnv(ctx, app, st.Name.ValueString())
	if client.IsNotFound(err) || (err == nil && e.Status == client.StatusArchived) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el entorno", err)
		return
	}
	r.fill(&st, app, e)
	if e.Status == client.StatusError {
		resp.Diagnostics.AddWarning("El entorno está en estado error", "No se aprovisionó correctamente; un apply lo reconcilia.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Update: los únicos cambios posibles son de comportamiento (on_destroy, adopt_existing...), que no llaman a la API; se reconcilia si no está activo.
func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := plan.Timeouts.Update(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	app := plan.App.ValueString()
	e, err := waitEnvActive(ctx, r.c, app, plan.Name.ValueString(), to)
	if err != nil {
		apiError(&resp.Diagnostics, "El entorno no está activo", err)
		return
	}
	r.fill(&plan, app, e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := st.Timeouts.Delete(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	app, name := st.App.ValueString(), st.Name.ValueString()
	if _, err := r.c.ArchiveEnv(ctx, app, name); err != nil {
		if !client.IsNotFound(err) {
			apiError(&resp.Diagnostics, "No se pudo archivar el entorno", err)
		}
		return
	}
	if st.OnDestroy.ValueString() == "purge" {
		if err := r.c.PurgeEnv(ctx, app, name); err != nil && !client.IsNotFound(err) {
			apiError(&resp.Diagnostics, "El entorno quedó archivado pero no se pudo eliminar de forma definitiva (on_destroy = \"purge\")",
				fmt.Errorf("%w. El recurso se conserva en el state: repite destroy cuando se cumpla la condición", err))
		}
	}
}

func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/entorno", &resp.Diagnostics)
	if p == nil {
		return
	}
	e, err := r.c.GetEnv(ctx, p[0], p[1])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar el entorno", err)
		return
	}
	m := environmentModel{OnDestroy: types.StringValue("archive"), AdoptExisting: types.BoolValue(false), RestoreArchived: types.BoolValue(false), Timeouts: nullTimeouts()}
	r.fill(&m, p[0], e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
