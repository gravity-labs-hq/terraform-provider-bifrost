package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// keepAfterCreate conserva el valor del state una vez creado el recurso: el atributo solo cuenta al crear (la API no permite cambiarlo o no
// es del recurso). Requiere que el atributo sea Optional+Computed.
type keepAfterCreate struct{ what string }

func KeepAfterCreate(what string) planmodifier.String { return keepAfterCreate{what} }

func (m keepAfterCreate) Description(context.Context) string {
	return "Solo se usa al crear: " + m.what
}
func (m keepAfterCreate) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m keepAfterCreate) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.StateValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

// keepSetAfterCreate es keepAfterCreate para conjuntos.
type keepSetAfterCreate struct{ what string }

func KeepSetAfterCreate(what string) planmodifier.Set { return keepSetAfterCreate{what} }

func (m keepSetAfterCreate) Description(context.Context) string {
	return "Solo se usa al crear: " + m.what
}
func (m keepSetAfterCreate) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m keepSetAfterCreate) PlanModifySet(_ context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	if req.State.Raw.IsNull() || req.StateValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}
