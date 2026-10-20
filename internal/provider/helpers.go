package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
)

// pollInterval es la espera entre consultas mientras un entorno se aprovisiona (las pruebas lo acortan).
var pollInterval = 3 * time.Second

// Duración por omisión de create/update/delete cuando el recurso no declara `timeouts`.
const defaultTimeout = 10 * time.Minute

// base da a los recursos y data sources el cliente de la API.
type base struct{ c *client.Client }

func (b *base) configure(data any, diags *diag.Diagnostics) {
	if data == nil {
		return
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Configuración del provider inesperada", fmt.Sprintf("se esperaba *client.Client y llegó %T", data))
		return
	}
	b.c = c
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	b.configure(req.ProviderData, &resp.Diagnostics)
}

// apiError agrega al diagnóstico un error de la API con contexto y, cuando ayuda, una pista de qué hacer.
func apiError(diags *diag.Diagnostics, summary string, err error) {
	detail := err.Error()
	var ae *client.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case 401:
			detail += "\n\nLa credencial no es válida o venció. Revisa la autenticación del provider (ver la documentación del provider bifrost)."
		case 403:
			detail += "\n\nLa identidad no tiene permiso para esta operación. Muchas operaciones de plataforma (crear Apps, redes, colas, runners, administradores) exigen una persona administradora: las cuentas de servicio y los vínculos de CI no son administradores."
		case 502:
			detail += "\n\nEl servidor pudo registrar el cambio pero fallar al aplicarlo en el cluster; ejecuta `terraform apply` de nuevo (reconcilia) o revisa el estado en el portal."
		}
	}
	diags.AddError(summary, detail)
}

// splitID parte un id compuesto "a/b/c" en n partes exactas; si no coincide, deja un error de importación claro.
func splitID(id string, n int, form string, diags *diag.Diagnostics) []string {
	parts := strings.Split(id, "/")
	if len(parts) != n {
		diags.AddError("Identificador de importación no válido", fmt.Sprintf("El id debe tener la forma %q (recibido %q).", form, id))
		return nil
	}
	for _, p := range parts {
		if p == "" {
			diags.AddError("Identificador de importación no válido", fmt.Sprintf("El id debe tener la forma %q (recibido %q): hay partes vacías.", form, id))
			return nil
		}
	}
	return parts
}

// ---- conjuntos y listas ----

func strSet(l []string) types.Set {
	vals := make([]attr.Value, 0, len(l))
	for _, s := range l {
		vals = append(vals, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, vals)
}

func strList(l []string) types.List {
	vals := make([]attr.Value, 0, len(l))
	for _, s := range l {
		vals = append(vals, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, vals)
}

func intSet(l []int64) types.Set {
	vals := make([]attr.Value, 0, len(l))
	for _, s := range l {
		vals = append(vals, types.Int64Value(s))
	}
	return types.SetValueMust(types.Int64Type, vals)
}

func setStrings(ctx context.Context, s types.Set) []string {
	if s.IsNull() || s.IsUnknown() {
		return []string{}
	}
	var out []string
	_ = s.ElementsAs(ctx, &out, false)
	if out == nil {
		out = []string{}
	}
	return out
}

func listStrings(ctx context.Context, s types.List) []string {
	if s.IsNull() || s.IsUnknown() {
		return []string{}
	}
	var out []string
	_ = s.ElementsAs(ctx, &out, false)
	if out == nil {
		out = []string{}
	}
	return out
}

func setInts(ctx context.Context, s types.Set) []int64 {
	if s.IsNull() || s.IsUnknown() {
		return []int64{}
	}
	var out []int64
	_ = s.ElementsAs(ctx, &out, false)
	if out == nil {
		out = []int64{}
	}
	return out
}

func mapStrings(ctx context.Context, m types.Map) map[string]string {
	out := map[string]string{}
	if m.IsNull() || m.IsUnknown() {
		return out
	}
	_ = m.ElementsAs(ctx, &out, false)
	return out
}

func strMap(m map[string]string) types.Map {
	vals := map[string]attr.Value{}
	for k, v := range m {
		vals[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, vals)
}

// sameSet compara dos listas como conjuntos.
func sameSet[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[T]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// ---- validadores (mismas reglas que la API) ----

// slugRe: letras minúsculas, números y guiones medios; empieza con letra; sin guiones dobles ni finales (es lo que deja Slug() del servidor).
var slugRe = regexp.MustCompile(`^[a-z](?:[a-z0-9]|-[a-z0-9])+$`)

func slugValidators(what string, min, max int) []validator.String {
	return []validator.String{
		stringvalidator.LengthBetween(min, max),
		stringvalidator.RegexMatches(slugRe, fmt.Sprintf("el nombre %s debe estar ya normalizado: minúsculas, números y guiones medios, empezar con una letra, sin guiones seguidos ni finales (la API lo normaliza y si no coincide habría diferencias eternas)", what)),
	}
}

var reservedApp = []string{"system", "default", "infra", "kube", "bifrost", "plane"}

func appNameValidators() []validator.String {
	return append(slugValidators("de la App", 2, 30), stringvalidator.NoneOf(reservedApp...))
}

func envNameValidators() []validator.String { return slugValidators("del entorno", 2, 16) }

var emailRe = regexp.MustCompile(`^[^@\s<>,;"]+@[^@\s<>,;"]+\.[^@\s<>,;"]+$`)

func emailValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthBetween(5, 254),
		stringvalidator.RegexMatches(emailRe, "el correo no es válido"),
		stringvalidator.RegexMatches(regexp.MustCompile(`^[^A-Z]*$`), "el correo debe ir en minúsculas (la API lo guarda así)"),
	}
}

var prefixRe = regexp.MustCompile(`^[a-z](?:[a-z0-9]|-[a-z0-9])*-?$`)

var poolNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var queueNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)

// ---- esperas ----

// waitActive espera a que un entorno quede `active`. Mientras esté en `provisioning` o `error` pide `reconcile` en cada vuelta (idempotente).
func waitEnvActive(ctx context.Context, c *client.Client, app, env string, timeout time.Duration) (client.Env, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last client.Env
	for i := 0; ; i++ {
		e, err := c.GetEnv(ctx, app, env)
		if err != nil {
			return last, err
		}
		last = e
		if e.Status == client.StatusActive {
			return e, nil
		}
		if e.Status == client.StatusArchived {
			return e, fmt.Errorf("el entorno %s/%s está archivado", app, env)
		}
		if i > 0 || e.Status == client.StatusError {
			if re, rerr := c.ReconcileEnv(ctx, app, env); rerr == nil {
				last = re
				if re.Status == client.StatusActive {
					return re, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			msg := ""
			if last.Error != nil {
				msg = ": " + *last.Error
			}
			return last, fmt.Errorf("el entorno %s/%s no quedó activo en %s (estado %q%s)", app, env, timeout, last.Status, msg)
		case <-time.After(pollInterval):
		}
	}
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

// rootAttr es un atajo para path.Root.
func rootAttr(n string) path.Path { return path.Root(n) }

type validatorString = validator.String

// timeoutsAttrTypes son los tipos del bloque `timeouts` (para dejarlo nulo al importar).
var timeoutsAttrTypes = map[string]attr.Type{"create": types.StringType, "update": types.StringType, "delete": types.StringType}

func nullTimeouts() timeouts.Value {
	return timeouts.Value{Object: types.ObjectNull(timeoutsAttrTypes)}
}

// warnKeyInState avisa en el plan de que una clave privada va a guardarse (sensible) en el state. Solo al crear o rotar.
func warnKeyInState(req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.Diagnostics.AddWarning("La clave privada irá al state de Terraform",
			"Este recurso guarda la clave privada (sensible) en el state. Protege el state: backend con cifrado en reposo y acceso restringido, o cifrado de state de OpenTofu; nunca lo subas a git. "+
				"Para no guardar claves usa la federación (`federated_platform`/`federated_app`, sin secretos) o el recurso efímero `bifrost_service_account_credential` (Terraform 1.10 o superior).")
	}
}

// restoreHint es el mensaje cuando el servidor niega restaurar (restaurar es solo de administradores).
const restoreHint = "Restaurar una App o un entorno archivado es solo de una persona administradora: una cuenta de servicio (de plataforma o de App) no puede. " +
	"Flujo manual: un administrador ejecuta `bifrost apps restore <app>` (o `bifrost envs restore <app> <entorno>`) y luego `terraform apply` (o `terraform import`) con restore_archived = false."
