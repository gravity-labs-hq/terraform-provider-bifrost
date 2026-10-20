package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

type quotaModel struct {
	CPU    types.String `tfsdk:"cpu"`
	Memory types.String `tfsdk:"memory"`
	Pods   types.Int64  `tfsdk:"pods"`
	PVCs   types.Int64  `tfsdk:"pvcs"`
}

var quotaAttrTypes = map[string]attr.Type{"cpu": types.StringType, "memory": types.StringType, "pods": types.Int64Type, "pvcs": types.Int64Type}

var cpuRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?m?$`)
var memRe = regexp.MustCompile(`^[0-9]+(Ki|Mi|Gi|Ti|K|M|G|T)?$`)

func cpuValidators() []validator.String {
	return []validator.String{stringvalidator.RegexMatches(cpuRe, "CPU no válida (ej. \"2\" o \"500m\")")}
}
func memValidators() []validator.String {
	return []validator.String{stringvalidator.RegexMatches(memRe, "memoria no válida (ej. \"4Gi\")")}
}

// quotaAttrs son los atributos de una cuota (cpu, memoria, pods, volúmenes) con los límites de la API.
func quotaAttrs(required bool) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"cpu":    schema.StringAttribute{Required: required, Optional: !required, Computed: !required, Description: "Núcleos solicitados, ej. `2` o `500m`.", Validators: cpuValidators()},
		"memory": schema.StringAttribute{Required: required, Optional: !required, Computed: !required, Description: "Memoria solicitada, ej. `4Gi`.", Validators: memValidators()},
		"pods":   schema.Int64Attribute{Required: required, Optional: !required, Computed: !required, Description: "Máximo de pods (1 a 500).", Validators: []validator.Int64{int64validator.Between(1, 500)}},
		"pvcs":   schema.Int64Attribute{Required: required, Optional: !required, Computed: !required, Description: "Máximo de volúmenes persistentes (0 a 100).", Validators: []validator.Int64{int64validator.Between(0, 100)}},
	}
}

// quotaObjectAttr es el atributo `quota` de un entorno: Optional+Computed (sin él hereda la cuota por omisión de la App).
func quotaObjectAttr() schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional: true, Computed: true,
		Description:   "Cuota del entorno. Sin indicarla hereda la cuota por omisión de la App. **No se puede cambiar después de crear el entorno** (la API no tiene esa operación).",
		Attributes:    quotaAttrs(true),
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
	}
}

func quotaObject(q client.Quota) types.Object {
	return types.ObjectValueMust(quotaAttrTypes, map[string]attr.Value{
		"cpu": types.StringValue(q.CPU), "memory": types.StringValue(q.Memory), "pods": types.Int64Value(q.Pods), "pvcs": types.Int64Value(q.PVCs)})
}

func quotaFromObject(ctx context.Context, o types.Object) (*client.Quota, diag.Diagnostics) {
	if o.IsNull() || o.IsUnknown() {
		return nil, nil
	}
	var m quotaModel
	d := o.As(ctx, &m, basetypes.ObjectAsOptions{})
	if d.HasError() {
		return nil, d
	}
	return &client.Quota{CPU: m.CPU.ValueString(), Memory: m.Memory.ValueString(), Pods: m.Pods.ValueInt64(), PVCs: m.PVCs.ValueInt64()}, d
}

func sameQuota(a, b client.Quota) bool { return a == b }

func quotaString(q client.Quota) string {
	return fmt.Sprintf("cpu=%s memory=%s pods=%d pvcs=%d", q.CPU, q.Memory, q.Pods, q.PVCs)
}
