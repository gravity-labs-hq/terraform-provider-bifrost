package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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

// ================= programación de carga =================

var (
	_ resource.Resource                   = &scheduleResource{}
	_ resource.ResourceWithConfigure      = &scheduleResource{}
	_ resource.ResourceWithImportState    = &scheduleResource{}
	_ resource.ResourceWithValidateConfig = &scheduleResource{}
)

func NewScheduleResource() resource.Resource { return &scheduleResource{} }

type scheduleResource struct{ base }

type scheduleModel struct {
	ID          types.String                      `tfsdk:"id"`
	App         types.String                      `tfsdk:"app"`
	Environment types.String                      `tfsdk:"environment"`
	Enabled     types.Bool                        `tfsdk:"enabled"`
	Timezone    types.String                      `tfsdk:"timezone"`
	Base        types.String                      `tfsdk:"base"`
	Profiles    map[string]scheduleProfileModel   `tfsdk:"profiles"`
	Weekly      []weeklyRuleModel                 `tfsdk:"weekly"`
	Exceptions  map[string]scheduleExceptionModel `tfsdk:"exceptions"`
}

type scheduleProfileModel struct {
	Name        types.String                     `tfsdk:"name"`
	Description types.String                     `tfsdk:"description"`
	Color       types.String                     `tfsdk:"color"`
	Icon        types.String                     `tfsdk:"icon"`
	Workloads   map[string]scheduleWorkloadModel `tfsdk:"workloads"`
}

type scheduleWorkloadModel struct {
	Min            types.Int64 `tfsdk:"min"`
	Max            types.Int64 `tfsdk:"max"`
	CPUm           types.Int64 `tfsdk:"cpu_m"`
	MemoryMiB      types.Int64 `tfsdk:"memory_mib"`
	CPULimitM      types.Int64 `tfsdk:"cpu_limit_m"`
	MemoryLimitMiB types.Int64 `tfsdk:"memory_limit_mib"`
}

type weeklyRuleModel struct {
	Days    types.Set    `tfsdk:"days"`
	From    types.Int64  `tfsdk:"from"`
	To      types.Int64  `tfsdk:"to"`
	Profile types.String `tfsdk:"profile"`
}

type scheduleExceptionModel struct {
	Label  types.String     `tfsdk:"label"`
	Ranges []hourRangeModel `tfsdk:"ranges"`
}

type hourRangeModel struct {
	From    types.Int64  `tfsdk:"from"`
	To      types.Int64  `tfsdk:"to"`
	Profile types.String `tfsdk:"profile"`
}

var (
	profileIDRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	profileColor  = regexp.MustCompile(`^(#[0-9a-f]{6})?$`)
	exceptionDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	tzRe          = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,2}$`)
)

// profileIcons es la lista cerrada de iconos que acepta el servidor.
var profileIcons = []string{"", "sun", "bolt", "moon", "power", "shield", "star", "cloud"}

// noEdgeSpace rechaza un texto con espacios al inicio o al final: el servidor lo recorta y el plan nunca convergería.
type noEdgeSpaceValidator struct{}

func noEdgeSpace() noEdgeSpaceValidator { return noEdgeSpaceValidator{} }
func (noEdgeSpaceValidator) Description(context.Context) string {
	return "sin espacios ni saltos de línea al inicio o al final"
}
func (v noEdgeSpaceValidator) MarkdownDescription(c context.Context) string { return v.Description(c) }
func (noEdgeSpaceValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	v := req.ConfigValue.ValueString()
	if strings.TrimSpace(v) != v {
		resp.Diagnostics.AddAttributeError(req.Path, "Espacios sobrantes", "El servidor recorta los espacios al inicio y al final: quítalos para que el plan converja.")
	}
	if strings.ContainsAny(v, "\n\r\t\x00") {
		resp.Diagnostics.AddAttributeError(req.Path, "Caracteres no válidos", "No se admiten saltos de línea ni tabuladores.")
	}
}

func (r *scheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schedule"
}

func (r *scheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	rr := func() []planmodifier.String { return []planmodifier.String{stringplanmodifier.RequiresReplace()} }
	zero := int64default.StaticInt64(0)
	hourFrom := []validator.Int64{int64validator.Between(0, 23)}
	hourTo := []validator.Int64{int64validator.Between(1, 24)}
	resp.Schema = schema.Schema{
		Description: "Programación de carga de un entorno: perfiles de réplicas y recursos (Normal, Pico, Noche, Apagado…) que cambian solos según la hora de la semana, con excepciones por fecha. " +
			"Reemplaza TODA la programación del entorno (perfiles, horario, excepciones, zona y `enabled`). Necesita ser owner de la App, administrador o una cuenta de plataforma con el scope `schedule:manage`. " +
			"El cambio manual temporal (override) no es declarativo: se pone desde el portal o la API y vence solo. La API no tiene borrado: destruir deja `enabled = false` y conserva los perfiles guardados. " +
			"Las cargas que un perfil nombra deben existir como Deployments del entorno; un perfil que no cabe en la cuota del entorno se rechaza al aplicar.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"app":         schema.StringAttribute{Required: true, Validators: appNameValidators(), PlanModifiers: rr()},
			"environment": schema.StringAttribute{Required: true, Validators: envNameValidators(), PlanModifiers: rr()},
			"enabled":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Si la programación rige. `false` la guarda sin aplicarla (y suelta las cargas que gestionaba)."},
			"timezone": schema.StringAttribute{Optional: true, Computed: true, Validators: []validator.String{stringvalidator.RegexMatches(tzRe, "zona horaria IANA, p. ej. America/Santiago")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description:   "Zona horaria IANA (`America/Santiago`). Todo el horario se lee en hora local de esta zona (los cambios de hora no duplican ni saltan perfiles). Si falta, la de la instalación."},
			"base": schema.StringAttribute{Required: true, Description: "Id del perfil que rige donde el horario no dice nada (debe existir en `profiles`)."},
			"profiles": schema.MapNestedAttribute{Required: true, Validators: []validator.Map{mapvalidator.SizeBetween(1, 12), mapvalidator.KeysAre(stringvalidator.RegexMatches(profileIDRe, "minúsculas, números y guiones; empieza con letra; hasta 24"))},
				Description: "Perfiles por id (minúsculas, números y guiones; empieza con letra; hasta 24 caracteres; 1 a 12). Cada uno fija, por carga, las réplicas y los recursos por pod.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":        schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 40), noEdgeSpace()}, Description: "Nombre visible (único entre los perfiles, sin distinguir mayúsculas)."},
					"description": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Validators: []validator.String{stringvalidator.LengthAtMost(200), noEdgeSpace()}},
					"color":       schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Validators: []validator.String{stringvalidator.RegexMatches(profileColor, "#rrggbb en minúsculas, o vacío")}, Description: "`#rrggbb` en minúsculas."},
					"icon":        schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Validators: []validator.String{stringvalidator.OneOf(profileIcons...)}, Description: "`sun`, `bolt`, `moon`, `power`, `shield`, `star` o `cloud`."},
					"workloads": schema.MapNestedAttribute{Optional: true, Validators: []validator.Map{mapvalidator.SizeAtMost(20)},
						Description: "Deployments del entorno que el perfil gestiona, por nombre (hasta 20). Las cargas que no nombra no se tocan. Los StatefulSet no se programan.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"min":              schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(0, 100)}, Description: "Réplicas mínimas. 0 junto con `max = 0` apaga la carga."},
							"max":              schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(0, 100)}, Description: "Réplicas máximas (si `max` > 0, `min` es al menos 1)."},
							"cpu_m":            schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Validators: []validator.Int64{int64validator.Between(0, 64000)}, Description: "CPU que pide cada pod (milinúcleos); 0 = no cambiar."},
							"memory_mib":       schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Validators: []validator.Int64{int64validator.Between(0, 262144)}, Description: "Memoria que pide cada pod (MiB); 0 = no cambiar."},
							"cpu_limit_m":      schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Validators: []validator.Int64{int64validator.Between(0, 64000)}, Description: "Límite de CPU por pod (milinúcleos); 0 = no cambiar. Nunca menor que lo pedido."},
							"memory_limit_mib": schema.Int64Attribute{Optional: true, Computed: true, Default: zero, Validators: []validator.Int64{int64validator.Between(0, 262144)}, Description: "Límite de memoria por pod (MiB); 0 = no cambiar."},
						}}},
				}}},
			"weekly": schema.ListNestedAttribute{Optional: true, Description: "Horario semanal como reglas: los `days` indicados, de la hora `from` (incluida) a la hora `to` (excluida), rigen `profile`. Una regla posterior pisa a una anterior; lo que ninguna cubre usa `base`. " +
				"Los tramos que cruzan la medianoche se escriben como dos reglas. Si alguien cambia el horario fuera de Terraform, el siguiente plan lo muestra como reglas equivalentes.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"days": schema.SetAttribute{Required: true, ElementType: types.StringType, Validators: []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.OneOf(dayNames[:]...))},
						Description: "`mon`, `tue`, `wed`, `thu`, `fri`, `sat`, `sun`."},
					"from":    schema.Int64Attribute{Required: true, Validators: hourFrom, Description: "Hora de inicio, 0 a 23."},
					"to":      schema.Int64Attribute{Required: true, Validators: hourTo, Description: "Hora de término (excluida), 1 a 24; mayor que `from`."},
					"profile": schema.StringAttribute{Required: true, Description: "Id de un perfil de `profiles`."},
				}}},
			"exceptions": schema.MapNestedAttribute{Optional: true, Validators: []validator.Map{mapvalidator.SizeAtMost(150), mapvalidator.KeysAre(stringvalidator.RegexMatches(exceptionDate, "fecha AAAA-MM-DD"))},
				Description: "Excepciones por fecha (`AAAA-MM-DD`): ese día, las horas indicadas usan otro perfil y pisan al horario semanal. Hasta 150, a lo más 400 días hacia adelante. " +
					"El servidor descarta las que pasaron hace más de 2 días: quítalas de la configuración.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"label": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Validators: []validator.String{stringvalidator.LengthAtMost(60), noEdgeSpace()}, Description: "Etiqueta (feriado, evento)."},
					"ranges": schema.ListNestedAttribute{Required: true, Validators: []validator.List{listvalidator.SizeAtLeast(1)}, Description: "Tramos de horas de ese día; las horas que no cubre siguen el horario semanal.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"from":    schema.Int64Attribute{Required: true, Validators: hourFrom},
							"to":      schema.Int64Attribute{Required: true, Validators: hourTo},
							"profile": schema.StringAttribute{Required: true},
						}}},
				}}},
		},
	}
}

// ValidateConfig comprueba lo que el servidor rechazaría o descartaría y produciría un plan que no converge: referencias a perfiles que no existen, tramos al revés,
// excepciones vencidas y reglas que no cubren ninguna hora.
func (r *scheduleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m scheduleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	known := func(v types.String) (string, bool) { return v.ValueString(), !v.IsNull() && !v.IsUnknown() }
	profiles := map[string]bool{}
	names := map[string]string{}
	for id, p := range m.Profiles {
		profiles[id] = true
		if n, ok := known(p.Name); ok {
			k := strings.ToLower(n)
			if other, dup := names[k]; dup {
				resp.Diagnostics.AddAttributeError(path.Root("profiles").AtMapKey(id).AtName("name"), "Nombre de perfil repetido", fmt.Sprintf("Los perfiles %q y %q se llaman igual (sin distinguir mayúsculas).", other, id))
			}
			names[k] = id
		}
		for w, st := range p.Workloads {
			wp := path.Root("profiles").AtMapKey(id).AtName("workloads").AtMapKey(w)
			if !dnsLabelRe.MatchString(w) {
				resp.Diagnostics.AddAttributeError(wp, "Nombre de carga no válido", fmt.Sprintf("%q no es un nombre de Deployment válido.", w))
			}
			if st.Min.IsUnknown() || st.Max.IsUnknown() {
				continue
			}
			mn, mx := st.Min.ValueInt64(), st.Max.ValueInt64()
			switch {
			case mx < mn:
				resp.Diagnostics.AddAttributeError(wp, "Réplicas incoherentes", "`max` no puede ser menor que `min`.")
			case mx > 0 && mn < 1:
				resp.Diagnostics.AddAttributeError(wp, "Réplicas incoherentes", "Con `max` > 0, `min` es al menos 1 (para apagar la carga pon `min` y `max` en 0).")
			}
			if !st.CPUm.IsUnknown() && !st.CPULimitM.IsUnknown() && st.CPULimitM.ValueInt64() > 0 && st.CPUm.ValueInt64() > st.CPULimitM.ValueInt64() {
				resp.Diagnostics.AddAttributeError(wp, "Límite menor que lo pedido", "`cpu_limit_m` no puede ser menor que `cpu_m`.")
			}
			if !st.MemoryMiB.IsUnknown() && !st.MemoryLimitMiB.IsUnknown() && st.MemoryLimitMiB.ValueInt64() > 0 && st.MemoryMiB.ValueInt64() > st.MemoryLimitMiB.ValueInt64() {
				resp.Diagnostics.AddAttributeError(wp, "Límite menor que lo pedido", "`memory_limit_mib` no puede ser menor que `memory_mib`.")
			}
		}
	}
	if len(m.Profiles) == 0 {
		return // desconocido o vacío: lo señalan los validadores del atributo
	}
	if b, ok := known(m.Base); ok && !profiles[b] {
		resp.Diagnostics.AddAttributeError(path.Root("base"), "El perfil base no existe", fmt.Sprintf("%q no es un perfil de `profiles`.", b))
	}
	for i, w := range m.Weekly {
		wp := path.Root("weekly").AtListIndex(i)
		if pr, ok := known(w.Profile); ok && !profiles[pr] {
			resp.Diagnostics.AddAttributeError(wp.AtName("profile"), "El perfil no existe", fmt.Sprintf("%q no es un perfil de `profiles`.", pr))
		}
		if !w.From.IsUnknown() && !w.To.IsUnknown() && w.From.ValueInt64() >= w.To.ValueInt64() {
			resp.Diagnostics.AddAttributeError(wp.AtName("to"), "Tramo vacío o al revés", "`to` debe ser mayor que `from` (un tramo que cruza la medianoche se escribe como dos reglas).")
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for date, ex := range m.Exceptions {
		ep := path.Root("exceptions").AtMapKey(date)
		if d, err := time.Parse("2006-01-02", date); err != nil || d.Format("2006-01-02") != date {
			resp.Diagnostics.AddAttributeError(ep, "Fecha no válida", fmt.Sprintf("%q no es una fecha AAAA-MM-DD.", date))
		} else if d.Before(today.AddDate(0, 0, -2)) {
			resp.Diagnostics.AddAttributeError(ep, "Excepción vencida", "Pasó hace más de 2 días: el servidor la descarta al guardar y el plan nunca convergería. Quítala de la configuración.")
		} else if d.After(today.AddDate(0, 0, 400)) {
			resp.Diagnostics.AddAttributeError(ep, "Excepción demasiado lejana", "El servidor no acepta excepciones a más de 400 días.")
		}
		for j, rg := range ex.Ranges {
			rp := ep.AtName("ranges").AtListIndex(j)
			if pr, ok := known(rg.Profile); ok && !profiles[pr] {
				resp.Diagnostics.AddAttributeError(rp.AtName("profile"), "El perfil no existe", fmt.Sprintf("%q no es un perfil de `profiles`.", pr))
			}
			if !rg.From.IsUnknown() && !rg.To.IsUnknown() && rg.From.ValueInt64() >= rg.To.ValueInt64() {
				resp.Diagnostics.AddAttributeError(rp.AtName("to"), "Tramo vacío o al revés", "`to` debe ser mayor que `from`.")
			}
		}
	}
}

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ---- modelo <-> API ----

func (r *scheduleResource) toAPI(ctx context.Context, m *scheduleModel) client.ScheduleSpec {
	s := client.ScheduleSpec{Enabled: m.Enabled.ValueBool(), Timezone: m.Timezone.ValueString(), Base: m.Base.ValueString(), Profiles: []client.ScheduleProfile{}, Exceptions: []client.ScheduleException{}}
	ids := make([]string, 0, len(m.Profiles))
	for id := range m.Profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := m.Profiles[id]
		sp := client.ScheduleProfile{ID: id, Name: p.Name.ValueString(), Description: p.Description.ValueString(), Color: p.Color.ValueString(), Icon: p.Icon.ValueString(), Workloads: map[string]client.ScheduleWorkload{}}
		for w, st := range p.Workloads {
			sp.Workloads[w] = client.ScheduleWorkload{Min: st.Min.ValueInt64(), Max: st.Max.ValueInt64(), CPUm: st.CPUm.ValueInt64(), MemMiB: st.MemoryMiB.ValueInt64(),
				LimCPUm: st.CPULimitM.ValueInt64(), LimMemMiB: st.MemoryLimitMiB.ValueInt64()}
		}
		s.Profiles = append(s.Profiles, sp)
	}
	s.Weekly = buildWeek(weeklyRules(ctx, m.Weekly))
	dates := make([]string, 0, len(m.Exceptions))
	for d := range m.Exceptions {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	for _, d := range dates {
		ex := m.Exceptions[d]
		s.Exceptions = append(s.Exceptions, client.ScheduleException{Date: d, Label: ex.Label.ValueString(), Hours: buildDay(hourRanges(ex.Ranges))})
	}
	return s
}

func weeklyRules(ctx context.Context, in []weeklyRuleModel) []weeklyRule {
	out := make([]weeklyRule, 0, len(in))
	for _, w := range in {
		out = append(out, weeklyRule{Days: setStrings(ctx, w.Days), From: int(w.From.ValueInt64()), To: int(w.To.ValueInt64()), Profile: w.Profile.ValueString()})
	}
	return out
}

func hourRanges(in []hourRangeModel) []hourRange {
	out := make([]hourRange, 0, len(in))
	for _, h := range in {
		out = append(out, hourRange{From: int(h.From.ValueInt64()), To: int(h.To.ValueInt64()), Profile: h.Profile.ValueString()})
	}
	return out
}

// fill vuelca lo que la API guarda en el modelo. Las partes que Terraform declara de otra forma que la API (el horario semanal y las horas de las excepciones) se
// conservan tal cual las escribió quien configura mientras describan lo mismo que la API; si no (alguien lo cambió fuera), se muestran como reglas canónicas.
func (r *scheduleResource) fill(ctx context.Context, m *scheduleModel, app, env string, v client.ScheduleView) {
	sp := v.Schedule
	m.ID, m.App, m.Environment = types.StringValue(app+"/"+env), types.StringValue(app), types.StringValue(env)
	m.Enabled, m.Timezone, m.Base = types.BoolValue(sp.Enabled), types.StringValue(sp.Timezone), types.StringValue(sp.Base)

	prior := m.Profiles
	m.Profiles = map[string]scheduleProfileModel{}
	for _, p := range sp.Profiles {
		pm := scheduleProfileModel{Name: types.StringValue(p.Name), Description: types.StringValue(p.Description), Color: types.StringValue(p.Color), Icon: types.StringValue(p.Icon)}
		if len(p.Workloads) > 0 {
			pm.Workloads = map[string]scheduleWorkloadModel{}
			for w, st := range p.Workloads {
				pm.Workloads[w] = scheduleWorkloadModel{Min: types.Int64Value(st.Min), Max: types.Int64Value(st.Max), CPUm: types.Int64Value(st.CPUm), MemoryMiB: types.Int64Value(st.MemMiB),
					CPULimitM: types.Int64Value(st.LimCPUm), MemoryLimitMiB: types.Int64Value(st.LimMemMiB)}
			}
		} else if old, ok := prior[p.ID]; ok {
			pm.Workloads = old.Workloads // vacío en la API: conserva null o {} según lo declarado
		}
		m.Profiles[p.ID] = pm
	}

	// Horario semanal.
	remote := sp.Weekly
	if len(remote) != 7 {
		remote = buildWeek(nil)
	}
	switch {
	case m.Weekly != nil && sameMatrix(buildWeek(weeklyRules(ctx, m.Weekly)), remote):
		// lo declarado describe lo mismo: se conserva
	default:
		rules := rulesOfWeek(remote)
		if len(rules) == 0 {
			m.Weekly = nil
			break
		}
		m.Weekly = make([]weeklyRuleModel, 0, len(rules))
		for _, ru := range rules {
			m.Weekly = append(m.Weekly, weeklyRuleModel{Days: strSet(ru.Days), From: types.Int64Value(int64(ru.From)), To: types.Int64Value(int64(ru.To)), Profile: types.StringValue(ru.Profile)})
		}
	}

	// Excepciones.
	priorEx := m.Exceptions
	m.Exceptions = nil
	for _, e := range sp.Exceptions {
		if m.Exceptions == nil {
			m.Exceptions = map[string]scheduleExceptionModel{}
		}
		em := scheduleExceptionModel{Label: types.StringValue(e.Label)}
		if old, ok := priorEx[e.Date]; ok && sameHours(buildDay(hourRanges(old.Ranges)), e.Hours) {
			em.Ranges = old.Ranges
		} else {
			for _, rg := range rangesOfDay(e.Hours) {
				em.Ranges = append(em.Ranges, hourRangeModel{From: types.Int64Value(int64(rg.From)), To: types.Int64Value(int64(rg.To)), Profile: types.StringValue(rg.Profile)})
			}
		}
		m.Exceptions[e.Date] = em
	}
}

func (r *scheduleResource) put(ctx context.Context, m *scheduleModel, diags *diag.Diagnostics) bool {
	app, env := m.App.ValueString(), m.Environment.ValueString()
	v, err := r.c.SetSchedule(ctx, app, env, r.toAPI(ctx, m))
	if err != nil {
		apiError(diags, "No se pudo guardar la programación", err)
		return false
	}
	r.fill(ctx, m, app, env, v)
	return true
}

func (r *scheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *scheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan scheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *scheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var st scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, env := st.App.ValueString(), st.Environment.ValueString()
	v, err := r.c.GetSchedule(ctx, app, env)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la programación", err)
		return
	}
	if !v.Configured { // nada guardado: el siguiente apply la crea
		resp.State.RemoveResource(ctx)
		return
	}
	r.fill(ctx, &st, app, env, v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &st)...)
}

// Delete: la API no tiene DELETE de la programación. Se deja guardada con enabled = false (el reconciliador suelta las cargas que gestionaba).
func (r *scheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var st scheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &st)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec := r.toAPI(ctx, &st)
	spec.Enabled = false
	_, err := r.c.SetSchedule(ctx, st.App.ValueString(), st.Environment.ValueString(), spec)
	if err != nil && !client.IsNotFound(err) && !client.IsConflict(err) {
		apiError(&resp.Diagnostics, "No se pudo desactivar la programación", err)
		return
	}
	resp.Diagnostics.AddWarning("La programación queda guardada, desactivada", "La API no borra programaciones: los perfiles y el horario siguen guardados con `enabled = false` y no rigen.")
}

func (r *scheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "app/entorno", &resp.Diagnostics)
	if p == nil {
		return
	}
	v, err := r.c.GetSchedule(ctx, p[0], p[1])
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo importar la programación", err)
		return
	}
	if !v.Configured {
		resp.Diagnostics.AddError("No hay programación guardada", fmt.Sprintf("El entorno %s/%s todavía no tiene programación: declárala y aplica en vez de importarla.", p[0], p[1]))
		return
	}
	var m scheduleModel
	r.fill(ctx, &m, p[0], p[1], v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
