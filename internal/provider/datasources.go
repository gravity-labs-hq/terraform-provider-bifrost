package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

type dsBase struct{ c *client.Client }

func (b *dsBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Configuración del provider inesperada", "se esperaba *client.Client")
		return
	}
	b.c = c
}

// ================= bifrost_app =================

func NewAppDataSource() datasource.DataSource { return &appDataSource{} }

type appDataSource struct{ dsBase }

type appDSModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	DisplayName  types.String `tfsdk:"display_name"`
	Description  types.String `tfsdk:"description"`
	Group        types.String `tfsdk:"group"`
	Status       types.String `tfsdk:"status"`
	Environments types.List   `tfsdk:"environments"`
	Quota        types.Object `tfsdk:"default_quota"`
	Owners       types.List   `tfsdk:"owners"`
}

func (d *appDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (d *appDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lee una App existente.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true},
			"name":          schema.StringAttribute{Required: true},
			"display_name":  schema.StringAttribute{Computed: true},
			"description":   schema.StringAttribute{Computed: true},
			"group":         schema.StringAttribute{Computed: true, Description: "Grupo de la App en «Personas y permisos» (vacío = sin grupo)."},
			"status":        schema.StringAttribute{Computed: true},
			"environments":  schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Nombres de los entornos no archivados."},
			"default_quota": schema.SingleNestedAttribute{Computed: true, Attributes: map[string]schema.Attribute{"cpu": schema.StringAttribute{Computed: true}, "memory": schema.StringAttribute{Computed: true}, "pods": schema.Int64Attribute{Computed: true}, "pvcs": schema.Int64Attribute{Computed: true}}},
			"owners":        schema.ListAttribute{Computed: true, ElementType: types.StringType},
		},
	}
}

func (d *appDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m appDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := d.c.GetApp(ctx, m.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la App", err)
		return
	}
	m.ID, m.DisplayName, m.Description, m.Status = types.StringValue(a.Name), types.StringValue(a.DisplayName), types.StringValue(a.Description), types.StringValue(a.Status)
	m.Group = types.StringValue(a.Group)
	var envs, owners []string
	for _, e := range a.Envs {
		if e.Status != client.StatusArchived {
			envs = append(envs, e.Name)
		}
	}
	for _, x := range a.Members {
		if x.Role == "owner" {
			owners = append(owners, x.Email)
		}
	}
	m.Environments, m.Owners, m.Quota = strList(envs), strList(owners), quotaObject(a.Quota)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= bifrost_apps =================

func NewAppsDataSource() datasource.DataSource { return &appsDataSource{} }

type appsDataSource struct{ dsBase }

type appsDSModel struct {
	ID    types.String `tfsdk:"id"`
	Names types.List   `tfsdk:"names"`
	Apps  types.List   `tfsdk:"apps"`
}

var appItemType = types.ObjectType{AttrTypes: map[string]attr.Type{"name": types.StringType, "display_name": types.StringType, "status": types.StringType}}

func (d *appsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_apps"
}

func (d *appsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lista las Apps visibles para la identidad (todas si es administradora).",
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Computed: true},
			"names": schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"apps": schema.ListNestedAttribute{Computed: true, NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{Computed: true}, "display_name": schema.StringAttribute{Computed: true}, "status": schema.StringAttribute{Computed: true}}}},
		},
	}
}

func (d *appsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	l, err := d.c.ListApps(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudieron listar las Apps", err)
		return
	}
	names := []string{}
	items := []attr.Value{}
	for _, a := range l {
		names = append(names, a.Name)
		items = append(items, types.ObjectValueMust(appItemType.AttrTypes, map[string]attr.Value{"name": types.StringValue(a.Name), "display_name": types.StringValue(a.DisplayName), "status": types.StringValue(a.Status)}))
	}
	m := appsDSModel{ID: types.StringValue("apps"), Names: strList(names), Apps: types.ListValueMust(appItemType, items)}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= bifrost_environment =================

func NewEnvironmentDataSource() datasource.DataSource { return &environmentDataSource{} }

type environmentDataSource struct{ dsBase }

type envDSModel struct {
	ID        types.String `tfsdk:"id"`
	App       types.String `tfsdk:"app"`
	Name      types.String `tfsdk:"name"`
	Status    types.String `tfsdk:"status"`
	Namespace types.String `tfsdk:"namespace"`
	Hosts     types.List   `tfsdk:"hosts"`
	Quota     types.Object `tfsdk:"quota"`
	Services  types.List   `tfsdk:"services"`
	Domains   types.Set    `tfsdk:"domains"`
	Internet  types.Bool   `tfsdk:"internet"`
}

func (d *environmentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (d *environmentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lee un entorno existente.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true}, "app": schema.StringAttribute{Required: true}, "name": schema.StringAttribute{Required: true},
			"status": schema.StringAttribute{Computed: true}, "namespace": schema.StringAttribute{Computed: true},
			"hosts":    schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"quota":    schema.SingleNestedAttribute{Computed: true, Attributes: map[string]schema.Attribute{"cpu": schema.StringAttribute{Computed: true}, "memory": schema.StringAttribute{Computed: true}, "pods": schema.Int64Attribute{Computed: true}, "pvcs": schema.Int64Attribute{Computed: true}}},
			"services": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Ids de las herramientas añadidas."},
			"domains":  schema.SetAttribute{Computed: true, ElementType: types.StringType},
			"internet": schema.BoolAttribute{Computed: true},
		},
	}
}

func (d *environmentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m envDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := d.c.GetEnv(ctx, m.App.ValueString(), m.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el entorno", err)
		return
	}
	svcs := []string{}
	for _, s := range e.Services {
		svcs = append(svcs, s.Service)
	}
	m.ID, m.Status, m.Namespace = types.StringValue(m.App.ValueString()+"/"+e.Name), types.StringValue(e.Status), types.StringValue(e.Namespace)
	m.Hosts, m.Quota, m.Services, m.Domains, m.Internet = strList(e.Hosts), quotaObject(e.Quota), strList(svcs), strSet(e.Network.Domains), types.BoolValue(e.Network.Internet)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= bifrost_service =================

func NewServiceDataSource() datasource.DataSource { return &serviceDataSource{} }

type serviceDataSource struct{ dsBase }

type serviceDSModel struct {
	ID          types.String `tfsdk:"id"`
	App         types.String `tfsdk:"app"`
	Environment types.String `tfsdk:"environment"`
	Service     types.String `tfsdk:"service"`
	SecretName  types.String `tfsdk:"secret_name"`
	Dedicated   types.Bool   `tfsdk:"dedicated"`
	Connection  types.Map    `tfsdk:"public_values"`
	SecretKeys  types.List   `tfsdk:"secret_keys"`
}

func (d *serviceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service"
}

func (d *serviceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Datos de conexión de una herramienta añadida a un entorno. La API entrega los valores NO secretos (host, puerto, base, usuario); " +
			"los secretos (contraseña, URL completa) solo viven en el Secret de Kubernetes `secret_name` y aquí aparecen únicamente sus nombres en `secret_keys`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true}, "app": schema.StringAttribute{Required: true}, "environment": schema.StringAttribute{Required: true},
			"service":       schema.StringAttribute{Required: true},
			"secret_name":   schema.StringAttribute{Computed: true},
			"dedicated":     schema.BoolAttribute{Computed: true},
			"public_values": schema.MapAttribute{Computed: true, Sensitive: true, ElementType: types.StringType, Description: "Valores de conexión no secretos de la API (marcados sensibles por prudencia: revelan la topología)."},
			"secret_keys":   schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Claves del Secret que son secretas y no se exponen."},
		},
	}
}

func (d *serviceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m serviceDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, env, id := m.App.ValueString(), m.Environment.ValueString(), m.Service.ValueString()
	e, err := d.c.GetEnv(ctx, app, env)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer el entorno", err)
		return
	}
	found := false
	for _, s := range e.Services {
		if s.Service == id {
			found = true
			m.Dedicated = types.BoolValue(s.Dedicated)
		}
	}
	if !found {
		resp.Diagnostics.AddError("La herramienta no está en el entorno", app+"/"+env+" no tiene "+id)
		return
	}
	c, err := d.c.ServiceConnection(ctx, app, env, id)
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la conexión", err)
		return
	}
	vals, secrets := map[string]string{}, []string{}
	for _, k := range c.Keys {
		if k.Secret {
			secrets = append(secrets, k.Key)
		} else {
			vals[k.Key] = k.Value
		}
	}
	m.ID, m.SecretName, m.Connection, m.SecretKeys = types.StringValue(app+"/"+env+"/"+id), types.StringValue(c.SecretName), strMap(vals), strList(secrets)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ================= bifrost_person =================

func NewPersonDataSource() datasource.DataSource { return &personDataSource{} }

type personDataSource struct{ dsBase }

type personDSModel struct {
	ID          types.String `tfsdk:"id"`
	Email       types.String `tfsdk:"email"`
	Admin       types.Bool   `tfsdk:"admin"`
	AdminSource types.String `tfsdk:"admin_source"`
	Apps        types.Map    `tfsdk:"apps"`
}

func (d *personDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_person"
}

func (d *personDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Vista de una persona (solo administradores): si es administradora y su rol en cada App. Las personas no se crean: aparecen al ser miembro, administradora o al iniciar sesión (brecha de API: no hay `POST /people`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true}, "email": schema.StringAttribute{Required: true},
			"admin": schema.BoolAttribute{Computed: true}, "admin_source": schema.StringAttribute{Computed: true},
			"apps": schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "App -> rol."},
		},
	}
}

func (d *personDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m personDSModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := d.c.GetPerson(ctx, m.Email.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "No se pudo leer la persona", err)
		return
	}
	apps := map[string]string{}
	for _, a := range p.Apps {
		apps[a.App] = a.Role
	}
	m.ID, m.Admin, m.AdminSource, m.Apps = types.StringValue(p.Email), types.BoolValue(p.Admin), types.StringValue(p.AdminSource), strMap(apps)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
