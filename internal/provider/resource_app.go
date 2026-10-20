package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

var (
	_ resource.Resource                = &appResource{}
	_ resource.ResourceWithConfigure   = &appResource{}
	_ resource.ResourceWithImportState = &appResource{}
	_ resource.ResourceWithModifyPlan  = &appResource{}
)

func NewAppResource() resource.Resource { return &appResource{} }

type appResource struct{ base }

type appModel struct {
	ID                    types.String   `tfsdk:"id"`
	Name                  types.String   `tfsdk:"name"`
	DisplayName           types.String   `tfsdk:"display_name"`
	Description           types.String   `tfsdk:"description"`
	Group                 types.String   `tfsdk:"group"`
	Owner                 types.String   `tfsdk:"owner"`
	Owners                types.Set      `tfsdk:"owners"`
	ManagedBy             types.String   `tfsdk:"managed_by"`
	FirstEnvironment      types.String   `tfsdk:"first_environment"`
	FirstEnvironmentQuota *quotaModel    `tfsdk:"first_environment_quota"`
	Status                types.String   `tfsdk:"status"`
	CreatedBy             types.String   `tfsdk:"created_by"`
	OnDestroy             types.String   `tfsdk:"on_destroy"`
	RestoreArchived       types.Bool     `tfsdk:"restore_archived"`
	Timeouts              timeouts.Value `tfsdk:"timeouts"`
}

func (r *appResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *appResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Una App de Bifröst. Al crearla la API exige al menos un entorno (`first_environment`, por omisión `prod`): declara después un `bifrost_environment` con `adopt_existing = true` para gestionarlo, o déjalo sin gestionar. " +
			"Destruir archiva la App (reversible); `on_destroy = \"purge\"` la elimina de verdad (exige retención cumplida y sin herramientas).",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name": schema.StringAttribute{Required: true, Description: "Nombre canónico (slug, 2 a 30). Forma parte del namespace de cada entorno: no se puede cambiar.", Validators: appNameValidators(), PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"display_name": schema.StringAttribute{Optional: true, Computed: true, Description: "Nombre visible (hasta 80 caracteres). Por omisión, el `name`.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators: []validatorString{stringvalidator.LengthBetween(1, 80)}},
			"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "Descripción (hasta 200 caracteres).", Validators: []validatorString{stringvalidator.LengthAtMost(200)}},
			"group": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Validators: []validatorString{stringvalidator.RegexMatches(groupRe, "minúsculas, números y guiones (sin guiones al inicio ni al final), hasta 32 caracteres; vacío = sin grupo")},
				Description: "Grupo de la App en «Personas y permisos» (slug opcional). Solo organiza la vista: no da ni quita permisos. Vacío = sin grupo."},
			"owner": schema.StringAttribute{Optional: true, Computed: true, Description: "Correo del owner inicial. Solo se usa al crear (después, gestiona los owners con `bifrost_member`). Por omisión, quien aplica.",
				PlanModifiers: []planmodifier.String{KeepAfterCreate("el owner se gestiona con bifrost_member")}, Validators: emailValidators()},
			"owners": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, PlanModifiers: []planmodifier.Set{KeepSetAfterCreate("los owners se gestionan con bifrost_member")},
				Description: "Owners humanos iniciales (1 a 5 correos en minúsculas). **Obligatorio si la autenticación es una cuenta de servicio de plataforma**: una App creada por una máquina siempre tiene personas responsables. Solo se usa al crear; excluyente con `owner`.",
				Validators:  []validator.Set{setvalidator.SizeBetween(1, 5), setvalidator.ValueStringsAre(emailValidators()...)}},
			"managed_by": schema.StringAttribute{Computed: true, Description: "Cuenta de servicio de plataforma que creó la App y la gestiona técnicamente (vacío si la creó una persona).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"first_environment": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("prod"), Description: "Entorno que crea la API junto con la App. Solo se usa al crear.",
				Validators: envNameValidators(), PlanModifiers: []planmodifier.String{KeepAfterCreate("el entorno inicial se gestiona con bifrost_environment")}},
			"first_environment_quota": schema.SingleNestedAttribute{Optional: true, Description: "Cuota del entorno inicial (si no, la de por omisión). Solo se usa al crear.", Attributes: quotaAttrs(true)},
			"status":                  schema.StringAttribute{Computed: true, Description: "Estado derivado de los entornos: `active`, `error`..."},
			"created_by":              schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"on_destroy": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("archive"),
				Description: "`archive` (por omisión): `terraform destroy` archiva la App y conserva sus datos durante la retención. `purge`: además la elimina de forma irreversible (la API exige que haya pasado la retención y que no queden herramientas).",
				Validators:  []validatorString{stringvalidator.OneOf("archive", "purge")}},
			"restore_archived": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Si ya existe una App archivada con ese nombre, restaurarla en vez de fallar con 409."},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// ModifyPlan: si la App no está sana (estado `error`/`provisioning`), el siguiente apply debe reconciliarla en sitio.
func (r *appResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var st appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var plan appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := !plan.DisplayName.IsUnknown() && !plan.DisplayName.Equal(st.DisplayName) || !plan.Description.Equal(st.Description) || !plan.Group.Equal(st.Group)
	if s := st.Status.ValueString(); (s != "" && s != client.StatusActive) || changed {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, rootAttr("status"), types.StringUnknown())...)
	}
}

func (r *appResource) fill(m *appModel, a client.App) {
	m.ID, m.Name = types.StringValue(a.Name), types.StringValue(a.Name)
	m.DisplayName, m.Description, m.Group = types.StringValue(a.DisplayName), types.StringValue(a.Description), types.StringValue(a.Group)
	m.Status, m.CreatedBy, m.ManagedBy = types.StringValue(a.Status), types.StringValue(a.CreatedBy), types.StringValue(a.ManagedBy)
	if m.Owners.IsNull() || m.Owners.IsUnknown() {
		var os []string
		for _, x := range a.Members {
			if x.Role == "owner" {
				os = append(os, x.Email)
			}
		}
		m.Owners = strSet(os)
	}
	if m.Owner.IsNull() || m.Owner.IsUnknown() {
		m.Owner = types.StringValue("")
		for _, x := range a.Members {
			if x.Role == "owner" {
				m.Owner = types.StringValue(x.Email)
				break
			}
		}
	}
}

func (r *appResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := plan.Timeouts.Create(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	name := plan.Name.ValueString()
	first := plan.FirstEnvironment.ValueString()
	cr := client.CreateAppReq{Name: name, Description: plan.Description.ValueString(), Owner: str(plan.Owner), Envs: []client.CreateEnvReq{{Name: first}}}
	if !plan.Owners.IsNull() && !plan.Owners.IsUnknown() {
		if str(plan.Owner) != "" {
			resp.Diagnostics.AddAttributeError(rootAttr("owners"), "owner y owners son excluyentes", "Usa solo `owners` (varios) o solo `owner`.")
			return
		}
		cr.Owners = setStrings(ctx, plan.Owners)
	}
	if q := plan.FirstEnvironmentQuota; q != nil {
		cr.Envs[0].Quota = &client.Quota{CPU: q.CPU.ValueString(), Memory: q.Memory.ValueString(), Pods: q.Pods.ValueInt64(), PVCs: q.PVCs.ValueInt64()}
	}
	a, err := r.c.CreateApp(ctx, cr)
	var createErr error
	switch {
	case err == nil:
	case client.IsConflict(err) && plan.RestoreArchived.ValueBool():
		ex, gerr := r.c.GetApp(ctx, name)
		if gerr != nil || ex.Status != client.StatusArchived {
			apiError(&resp.Diagnostics, "No se pudo crear la App", err)
			return
		}
		if a, err = r.c.RestoreApp(ctx, name); err != nil {
			if client.IsForbidden(err) {
				err = fmt.Errorf("%w. %s", err, restoreHint)
			}
			apiError(&resp.Diagnostics, "La App estaba archivada y no se pudo restaurar", err)
			return
		}
	case isStatus(err, 502):
		// El servidor crea la App aunque falle el aprovisionamiento: queda en `error` y se reintenta con reconcile.
		var gerr error
		if a, gerr = r.c.GetApp(ctx, name); gerr != nil {
			apiError(&resp.Diagnostics, "No se pudo crear la App", err)
			return
		}
		createErr = err
	default:
		if client.IsConflict(err) {
			err = fmt.Errorf("%w. Si existe archivada, usa restore_archived = true; si existe y es tuya, impórtala con `terraform import`", err)
		}
		apiError(&resp.Diagnostics, "No se pudo crear la App", err)
		return
	}
	// Espera a que los entornos queden activos (reconcilia si hace falta).
	if _, werr := waitEnvActive(ctx, r.c, name, first, to); werr != nil {
		createErr = werr
	} else {
		createErr = nil
		if a2, gerr := r.c.GetApp(ctx, name); gerr == nil {
			a = a2
		}
	}
	if dn := str(plan.DisplayName); (dn != "" && dn != a.DisplayName) || plan.Description.ValueString() != a.Description || plan.Group.ValueString() != a.Group {
		s := client.SettingsReq{}
		if g := plan.Group.ValueString(); g != a.Group {
			s.Group = &g
		}
		if dn != "" && dn != a.DisplayName {
			s.DisplayName = &dn
		}
		if d := plan.Description.ValueString(); d != a.Description {
			s.Description = &d
		}
		if a2, uerr := r.c.UpdateApp(ctx, name, s); uerr == nil {
			a = a2
		} else {
			apiError(&resp.Diagnostics, "La App se creó pero no se pudieron fijar sus datos descriptivos", uerr)
		}
	}
	r.fill(&plan, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if createErr != nil {
		apiError(&resp.Diagnostics, "La App se creó pero no quedó sana (el recurso queda marcado como dañado y se reintentará)", createErr)
	}
}

func isStatus(err error, code int) bool {
	var ae *client.APIError
	return errors.As(err, &ae) && ae.Status == code
}

func (r *appResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.c.GetApp(ctx, st.Name.ValueString())
	if client.IsNotFound(err) || (err == nil && a.Status == client.StatusArchived) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la App", err)
		return
	}
	r.fill(&st, a)
	if a.Status == client.StatusError {
		resp.Diagnostics.AddWarning("La App está en estado error", "Algún entorno no se aprovisionó. Aplica de nuevo (el entorno lo reconcilia) o revisa el portal.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

func (r *appResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, st appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := st.Name.ValueString()
	s := client.SettingsReq{}
	if dn := str(plan.DisplayName); dn != "" && dn != st.DisplayName.ValueString() {
		s.DisplayName = &dn
	}
	if d := plan.Description.ValueString(); d != st.Description.ValueString() {
		s.Description = &d
	}
	if g := plan.Group.ValueString(); g != st.Group.ValueString() {
		s.Group = &g
	}
	a := client.App{}
	var err error
	if st.Status.ValueString() != client.StatusActive {
		to, d := plan.Timeouts.Update(ctx, defaultTimeout)
		resp.Diagnostics.Append(d...)
		cctx, cancel := context.WithTimeout(ctx, to)
		defer cancel()
		cur, gerr := r.c.GetApp(cctx, name)
		if gerr != nil {
			apiError(&resp.Diagnostics, "No se pudo leer la App", gerr)
			return
		}
		for _, e := range cur.Envs {
			if e.Status == client.StatusArchived {
				continue
			}
			if _, werr := waitEnvActive(cctx, r.c, name, e.Name, to); werr != nil {
				apiError(&resp.Diagnostics, "No se pudo reconciliar la App", werr)
				return
			}
		}
	}
	if s.DisplayName != nil || s.Description != nil || s.Group != nil {
		if a, err = r.c.UpdateApp(ctx, name, s); err != nil {
			apiError(&resp.Diagnostics, "No se pudo actualizar la App", err)
			return
		}
	} else if a, err = r.c.GetApp(ctx, name); err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la App", err)
		return
	}
	plan.Owner, plan.Owners = st.Owner, st.Owners
	r.fill(&plan, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	to, d := st.Timeouts.Delete(ctx, defaultTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	name := st.Name.ValueString()
	if _, err := r.c.ArchiveApp(ctx, name); err != nil {
		if client.IsNotFound(err) {
			return
		}
		apiError(&resp.Diagnostics, "No se pudo archivar la App", err)
		return
	}
	if st.OnDestroy.ValueString() != "purge" {
		return
	}
	if err := r.c.PurgeApp(ctx, name); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "La App quedó archivada pero no se pudo eliminar de forma definitiva (on_destroy = \"purge\")", fmt.Errorf("%w. El recurso se conserva en el state: vuelve a ejecutar destroy cuando se cumpla la condición (retención, herramientas quitadas)", err))
	}
}

func (r *appResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	a, err := r.c.GetApp(ctx, req.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la App", err)
		return
	}
	first := "prod"
	if len(a.Envs) > 0 {
		first = a.Envs[0].Name
	}
	m := appModel{OnDestroy: types.StringValue("archive"), RestoreArchived: types.BoolValue(false), FirstEnvironment: types.StringValue(first),
		Owner: types.StringNull(), Owners: types.SetNull(types.StringType), Timeouts: nullTimeouts()}
	r.fill(&m, a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
