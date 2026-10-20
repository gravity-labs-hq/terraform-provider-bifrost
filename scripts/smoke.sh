#!/usr/bin/env bash
# Smoke real: compila el provider y el servidor simulado, y ejecuta el terraform del PATH (init no hace falta con dev_overrides) sobre
# examples/tienda: plan -> apply -> plan sin cambios -> destroy. Sin red ni cluster. Falla ante cualquier error.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v terraform >/dev/null || { echo "terraform no está en el PATH" >&2; exit 2; }

work="$(mktemp -d)"
trap 'kill "${fake_pid:-0}" 2>/dev/null || true; wait 2>/dev/null || true; rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/ws"

go build -o "$work/bin/terraform-provider-bifrost" .
go build -o "$work/fakeplane" ./cmd/fakeplane

"$work/fakeplane" >"$work/url" 2>"$work/fake.log" &
fake_pid=$!
for _ in $(seq 1 50); do [ -s "$work/url" ] && break; sleep 0.1; done
url="$(head -n1 "$work/url")"
[ -n "$url" ] || { echo "fakeplane no arrancó" >&2; cat "$work/fake.log" >&2; exit 1; }

cat >"$work/terraformrc" <<RC
provider_installation {
  dev_overrides {
    "gravity-labs-hq/bifrost" = "$work/bin"
    "registry.opentofu.org/gravity-labs-hq/bifrost" = "$work/bin"
  }
  direct {}
}
RC
export TF_CLI_CONFIG_FILE="$work/terraformrc"
export BIFROST_API="$url"
export BIFROST_TOKEN="token-directo-ejemplo"
export TF_IN_AUTOMATION=1
export TF_CLI_ARGS="-no-color"
root="$PWD"
cp examples/tienda/main.tf "$work/ws/main.tf"
mkdir -p "$work/boot"; cp examples/bootstrap/main.tf "$work/boot/main.tf"

# Fase 0 (BOOTSTRAP, persona administradora): crea la cuenta de plataforma y recoge su clave.
cd "$work/boot"
run() { # run <archivo-de-salida> <comando...>: muestra la salida si el comando falla
  local out="$1"; shift
  "$@" >"$out" 2>&1 || { cat "$out" >&2; exit 1; }
}
echo "== bootstrap (administrador)"
run boot.txt terraform apply -auto-approve -input=false
terraform output -raw clave_ci >"$work/clave.json"
chmod 600 "$work/clave.json"
cd "$work/ws"
# A partir de aquí Terraform corre SOLO con la cuenta de plataforma (sin token de administrador).
unset BIFROST_TOKEN
export BIFROST_KEY_FILE="$work/clave.json"
echo "== el administrador sigue siendo necesario para lo que la cuenta no puede (debe fallar con 403)"
cat >"$work/ws/admin.tf" <<'TF'
resource "bifrost_admin" "x" { email = "yo@example.org" }
TF
if terraform plan -input=false >nofalla.txt 2>&1 && terraform apply -auto-approve -input=false -target=bifrost_admin.x >nofalla.txt 2>&1; then echo "ERROR: una cuenta de plataforma creó un administrador" >&2; exit 1; fi
grep -q "persona administradora" nofalla.txt && echo "denegado"
rm -f "$work/ws/admin.tf"

run() { # run <archivo-de-salida> <comando...>: muestra la salida si el comando falla
  local out="$1"; shift
  "$@" >"$out" 2>&1 || { cat "$out" >&2; exit 1; }
}
expect() { grep -E "$2" "$1" || { echo "no se encontró /$2/ en $1:" >&2; cat "$1" >&2; exit 1; }; }
echo "== plan";            run plan.txt terraform plan -input=false -out=plan.bin; expect plan.txt "^Plan:"
echo "== apply";           run apply.txt terraform apply -input=false plan.bin; expect apply.txt "^Apply complete"
echo "== plan sin cambios"; run again.txt terraform plan -input=false -detailed-exitcode; echo "sin cambios"
echo "== la clave es sensible en la salida"
terraform output | grep -q "clave_deploy *= <sensitive>" && echo "oculta" || { echo "ERROR: la salida sensible se imprimió" >&2; exit 1; }
if grep -q "BEGIN PRIVATE KEY" apply.txt plan.txt; then echo "ERROR: la clave privada salió en el log" >&2; exit 1; fi
echo "== importar lo ya creado en un estado nuevo: 0 cambios, 0 reemplazos"
mkdir -p "$work/ws2"
# Al importar no se usa adopt_existing (es para entornos que la API crea junto con la App y aún no están en el estado).
grep -v "adopt_existing" main.tf >"$work/ws2/main.tf"
terraform show -json >"$work/state.json"
python3 - "$work/state.json" >>"$work/ws2/imports.tf" <<'PY'
import json, sys
st = json.load(open(sys.argv[1]))
imp = {"bifrost_app", "bifrost_environment", "bifrost_service", "bifrost_service_account", "bifrost_member", "bifrost_binding", "bifrost_quota",
       "bifrost_network", "bifrost_host_rules", "bifrost_access", "bifrost_queue", "bifrost_runner_pool", "bifrost_admin"}
def walk(m):
    for r in m.get("resources", []):
        if r["type"] in imp and r.get("mode") == "managed":
            addr = r["address"]
            print('import {\n  to = %s\n  id = "%s"\n}\n' % (addr, r["values"]["id"]))
    for c in m.get("child_modules", []):
        walk(c)
walk(st["values"]["root_module"])
PY
cd "$work/ws2"
terraform plan -input=false -no-color >plan2.txt 2>&1 || { cat plan2.txt >&2; exit 1; }
expect plan2.txt "to import, 0 to add, 0 to change, 0 to destroy"
echo "importación sin cambios ni reemplazos"
cd "$work/ws"
echo "== destroy de una herramienta (necesita confirm_data_loss: debe fallar sin él)"
if terraform destroy -auto-approve -input=false -target=bifrost_service.postgres_prod >destroy.txt 2>&1; then echo "ERROR: el destroy no debió pasar sin confirm_data_loss" >&2; exit 1; fi
grep -q "Protección contra pérdida de datos" destroy.txt && echo "protegido"
sed -i.bak 's#service     = "postgres"#service     = "postgres"\n  confirm_data_loss = true#; s#service     = "redis"#service     = "redis"\n  confirm_data_loss = true#' main.tf
run apply2.txt terraform apply -auto-approve -input=false
run destroy.txt terraform destroy -auto-approve -input=false; expect destroy.txt "^Destroy complete"
echo "== bootstrap destroy (administrador)"
cd "$work/boot"; export BIFROST_TOKEN="token-directo-ejemplo"; unset BIFROST_KEY_FILE
run bootd.txt terraform destroy -auto-approve -input=false
echo "SMOKE OK ($(terraform version | head -n1))"
