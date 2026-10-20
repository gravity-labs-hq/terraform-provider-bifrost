# Diseño del provider de Terraform para Bifröst

Base: `docs/inventario-api.md` (rutas, validaciones y brechas leídas del código del servidor). Framework: `terraform-plugin-framework` (protocolo v6),
módulo Go propio (`go.mod` aparte; no arrastra pgx/nats/mongo del servidor).

## 1. Autenticación

El provider habla con **plane** (`https://plane.<dominio>`), no con el portal: `/v1/*` con `Authorization: Bearer`, mismas rutas y handlers que `/api/*`.
Formas soportadas (exactamente **una**; si hay varias el provider falla en vez de elegir):

| Forma | Atributos / variable de entorno | Alcance real |
|---|---|---|
| Persona administradora con ID token del IdP | `id_token` o `id_token_env` + `id_token_grant="user"` · `BIFROST_ID_TOKEN`, `BIFROST_ID_TOKEN_ENV`, `BIFROST_ID_TOKEN_GRANT` | **Toda la plataforma** |
| Token directo | `token` · `BIFROST_TOKEN` | el de quien lo emitió (dura ~1 h, no se renueva) |
| Cuenta de servicio con clave | `key_file` / `key_json` · `BIFROST_KEY_FILE`, `BIFROST_KEY_JSON` (la `api` sale de la clave) | solo su App y entornos |
| Federación del CI (GitLab) | `id_token`/`id_token_env` + `federated_app` (grant `federated`) | solo la App del vínculo |

Comunes: `api` / `BIFROST_API` (`https`; `http` solo para localhost/127.0.0.1), `ca_file`/`ca_pem` (`BIFROST_CA_FILE`, `BIFROST_CA_PEM`) para una CA privada.
**No existe `insecure`**: la verificación TLS no se puede desactivar. `id_token_env` guarda solo el *nombre* de la variable y se lee en cada canje.

**Restricción del servidor:** una identidad de máquina *nunca* es administradora; una cuenta de servicio **de una App** o un vínculo de CI de una App solo llegan a su App (`machineAllowed`).
Para administrar la plataforma desde un CI existe la **cuenta de servicio de plataforma** con scopes acotados (§6): no es administradora, y por eso `bifrost_admin` y `bifrost_platform_account` siguen exigiendo persona administradora. Los 403 lo explican.

**Canje de token: se reimplementa** (~150 líneas en `internal/client/auth.go`) en vez de importar `internal/cli/auth` del servidor. Importarlo es posible por la regla de
`internal/` (el módulo cuelga de la misma ruta) pero exigiría `replace` y metería todo el grafo del servidor (pgx, nats, mongo, k8s) en el binario. El contrato
(JWT EdDSA, `kid=private_key_id`, `iss=sub=cuenta`, `aud=token_uri`, `exp<=5 min`, `jti`; `/auth/token` con `grant`) está fijado por el servidor simulado
`internal/fakeapi`, que verifica la firma como lo hace plane. Si cambia `internal/plane/wire`, hay que actualizar ambos.

## 2. Recursos y data sources

IDs compuestos para `terraform import` (`app/entorno/...`). `S` = atributo sensible. Todos validan nombres con las reglas de la API (slugs ya normalizados: la API normaliza
y un nombre no canónico produciría diferencias eternas, por eso se rechaza en el plan).

| Recurso | Id de importación | Atributos clave | Update | Destroy |
|---|---|---|---|---|
| `bifrost_app` | `app` | name\*, display_name, description, group, owner†, first_environment†, first_environment_quota†, on_destroy, restore_archived | PATCH | **archiva** (purge opcional) |
| `bifrost_environment` | `app/env` | app\*, name\*, quota (inmutable), adopt_existing, restore_archived, on_destroy | no (reconcilia) | **archiva** (purge opcional) |
| `bifrost_service` | `app/env/servicio` | app, environment, service, options (inmutable), dedicated, confirm_data_loss | no | `confirm` solo si `confirm_data_loss=true` |
| `bifrost_service_account` | `app/cuenta` | app, name, role, environments, expires_in_days, **key_json S** | reemplazo | revoca |
| `bifrost_service_account_key` | — | app, account, keepers, keep_previous, revoke_on_destroy, **key_json S** | reemplazo (rota o añade) | solo del state; con `revoke_on_destroy` revoca esa llave |
| `bifrost_platform_account` | `nombre` | name, scopes, apps, app_prefix, expires_in_days, keepers, ci_binding, **key_json S** | rota la clave | revoca (**solo persona administradora**) |
| `bifrost_member` | `app/correo` | app, email, role | PUT rol | DELETE |
| `bifrost_binding` | `app/id-vínculo` | app, project_path, role, issuer, claims, ref_protected, environments, branches | **en sitio** (PUT; app/kind/issuer reemplazan) | DELETE |
| `bifrost_quota` | `app` | app, cpu, memory, pods, pvcs (cuota por omisión de entornos *nuevos*) | PUT | solo del state |
| `bifrost_network` | `app/env` | domains, internet, internet_ports, egress | PUT (reemplaza la red) | PUT vacío |
| `bifrost_host_rules` | `app/env/host` | ip_allow/deny (forma canónica), ip_notes, rate_limit, basic_auth (**password S**), cors, www, compress, https_redirect, timeout_seconds, maintenance… | PUT (completo) | PUT vacío |
| `bifrost_access` | `app/env/origen/servicio/puerto` | app, environment, from, service, port | reemplazo | DELETE |
| `bifrost_scaling` | `app/env/Tipo/carga` | kind, workload, horizontal{}, vertical{} | PUT | solo del state |
| `bifrost_schedule` | `app/env` | app, environment, enabled, timezone, base, profiles{}, weekly[] (reglas días + horas), exceptions{} (por fecha) | PUT (reemplaza todo) | PUT con `enabled=false` (la API no borra) |
| `bifrost_queue` | `tipo/nombre` | kind, name, display, hidden, jetstream{}, confirm_data_loss | PUT display/hidden | DELETE (`confirm_data_loss` en jetstream) |
| `bifrost_replication_link` | — (pendiente) | name, source, destination, mode, app, wait_for_approval | reemplazo | solo del state |
| `bifrost_runner_pool` | `nombre` | name, labels, scope, min, max, origins, paused, app | PUT | DELETE |
| `bifrost_admin` | `correo` | email, source | reemplazo | DELETE |

† solo cuenta al crear (la API crea un entorno con la App y el owner se gestiona con `bifrost_member`). Data sources: `bifrost_app`, `bifrost_apps`, `bifrost_environment`,
`bifrost_service` (`public_values` **S** = claves no secretas del Secret de conexión; las secretas solo aparecen por nombre en `secret_keys`: la API no las entrega), `bifrost_person`.

**Secretos.** `key_json` (la clave privada de la cuenta) se guarda **sensible en el state** y se avisa con un warning en cada creación/rotación; la API solo la entrega al crearla. Al importar queda vacía.
`basic_auth.password` es sensible y de solo entrada (la API nunca la devuelve: no hay drift de contraseña). Ningún log del provider incluye cuerpos, cabeceras ni tokens (solo `MÉTODO ruta -> código`); hay pruebas.

## 3. Semántica

- **Create**: la API aprovisiona *dentro* de la petición. Tras crear, el provider espera a que el entorno esté `active` (`reconcile` + reintento hasta `timeouts.create`, 10 min por omisión). Un 502
  deja la App/entorno creada en `error`: se lee, se reconcilia y, si no sana, el recurso queda **tainted** con el motivo (reemplazarlo requiere `restore_archived=true`, porque destruir archiva).
- **Read**: `GET` y compara; 404 o `archived` ⇒ se quita del state (el plan lo recrea). Un entorno en `error` se señala con un warning y el siguiente `apply` lo reconcilia en sitio.
  Detecta drift de todo lo que la API devuelve; **no** de `service.options`, `host_rules…password`, ni de `first_environment/owner` (solo al crear).
- **Update**: solo lo que la API permite editar; el resto es `RequiresReplace`. La cuota de un entorno no tiene API: cambiarla da un error claro en el *plan* (brecha #1).
- **Import**: ids compuestos; valores no recuperables quedan vacíos con aviso.
- **Orden**: app → environment → service / service_account / binding / member / network → host_rules / access / scaling; las referencias (`bifrost_app.x.name`) lo expresan.

## 4. Eliminaciones destructivas

La API es de dos fases (archivar → purgar con `confirm` tras la retención). El provider lo respeta y **no purga por omisión**:
`on_destroy = "archive"` (por defecto) conserva los datos; `"purge"` archiva y luego `DELETE ?purge=true&confirm=<nombre>`; si la retención no se cumplió o quedan herramientas, el destroy
falla **conservando el recurso en el state** con el motivo y la fecha, y se repite más tarde. Herramientas (`bifrost_service`) y colas JetStream borran datos: exigen `confirm_data_loss = true`
*ya aplicado* (destruir o reemplazar sin eso falla en ese recurso sin borrar nada). Complementa con `lifecycle { prevent_destroy = true }` en producción.

## 5. Decisiones ambiguas (decididas)

1. `bifrost_app` exige `first_environment` (default `prod`) porque la API siempre crea uno; `bifrost_environment.adopt_existing` lo adopta. Alternativa descartada: ocultar la creación (rompería el 1:1 con la API).
2. `bifrost_person` es solo data source (no hay `POST /people`); `bifrost_replication_link` y `bifrost_quota`/`scaling` borran solo del state (la API no tiene el inverso). Todo documentado en el recurso.
3. `service_account.key_json` vive en el recurso de la cuenta (única vez que existe la clave) y `service_account_key` rota; destruir la clave no la revoca (no hay endpoint).
4. Reintentos solo para lecturas (GET) ante 429/502/503; las escrituras nunca se reintentan solas.

## 6. Cuenta de servicio de plataforma (Terraform en CI sin ser administrador)

**Problema.** `bifrost_app`, red, cuota, colas, runners, miembros, cuentas y vínculos exigían persona administradora; un CI no la tiene. **Decisión.** Una identidad de máquina nueva con **scopes explícitos**,
NO un administrador disfrazado: `identity.IsAdmin()` sigue siendo falso para toda máquina (invariante intacta y probada); los permisos son exactamente sus scopes, con *default-deny*.

**Scopes** (lista cerrada en `internal/scope`; lo que no está no se puede otorgar): `apps:read`, `apps:create`, `apps:manage` (entornos, herramientas, escalado, enlaces, archivar), `quota:manage`, `network:manage`
(dominios, salida, reglas por host, accesos entre Apps), `members:manage`, `identities:manage` (cuentas de servicio y vínculos de CI **de las Apps permitidas**), `queues:read|manage`, `runners:read|manage`,
`replication:read|propose`, `schedule:manage` (programación de carga: ver abajo). Cada `:manage` incluye su lectura (se guarda expandida). **No existen** scopes sobre administradores, personas, cuentas de plataforma (crear/rotar/revocar), otorgamiento de scopes,
claves SSH, túneles, respaldos, auditoría ni avisos; tampoco purgar (`?purge=true`) ni restaurar (revierte decisiones de un administrador).

**Alcance por App.** Una App es "permitida" si **la creó la cuenta** (`managed_by`), está en su lista `apps`, o su nombre empieza con `app_prefix` (comparación literal con `strings.HasPrefix`, sin regex; el nombre se
compara ya normalizado a slug). Fuera de eso la App "no existe" (404) en TODAS las rutas. Las referencias entre recursos (App de un pool o enlace, `from` de un acceso) deben ser permitidas (anti *confused deputy*).
Colas, pools y enlaces no cuelgan de una App: sus scopes **exigen `app_prefix`** al crear la cuenta y los nombres deben empezar con él; las listas se filtran por prefijo.

**Reglas de seguridad de la cuenta (decididas, ver §7):** `apps:create` **exige `app_prefix`**; `identities:manage` es **opt-in** (`--allow-identities` / `allow_identities`) y exige lista `apps` o prefijo (nunca sin límite); `apps:create` y `members:manage`
exigen `allowed_domains` (dominios de correo a los que puede dar rol de owner/miembro; jamás otra cuenta de plataforma ni `@platform`). Las cuentas creadas con reglas anteriores siguen autenticando pero el servidor les **quita en cada petición** los scopes
que ya no pueden tener y no se pueden renovar (se recrean con otro nombre). Restaurar Apps/entornos archivados es **solo de personas administradoras** (flujo manual: `bifrost apps restore <app>` / `bifrost envs restore <app> <entorno>`); `restore_archived` en el provider devuelve ese flujo.

**Propiedad.** La App creada por la cuenta queda con `managed_by = <cuenta>` (gestora técnica, no es un miembro) y con los `owners` humanos indicados (**obligatorios**, 1 a 5: siempre hay una persona responsable).
Fijar cuotas al crear exige además `quota:manage`; sin él se hereda la cuota por omisión. Tope: 50 Apps por cuenta.

**Identidades que crea la cuenta** (cuentas de servicio de una App, vínculos de CI, owners/miembros): entornos **explícitos** (nunca «todos, presentes y futuros»); vínculos solo con `ref_protected`, condición `environment`, proyecto exacto y entornos explícitos (los vínculos no caducan, por eso las claims son estrictas); owners y miembros solo en `allowed_domains`. Cada creación deja auditoría `platform.identity` con el actor `sa:<nombre>@platform` y un **aviso `priority`** a los administradores (evento de la lista blanca de `internal/notify`, activo por omisión). No hay tope de caducidad ni límite de cantidad para estas identidades (decisión de producto).

**Gestión.** Solo una persona administradora crea, lista, rota y revoca (`POST/GET /v1/platform-accounts`, `…/{n}/keys`, `DELETE`; CLI `bifrost sa create --platform --scope … [--apps a,b] [--app-prefix p-] --expires-in-days N
[--ci-project g/r --ci-environment production] --out clave.json`; Terraform: `bifrost_platform_account`). **Caducidad obligatoria** (1–365 días); rotar renueva la caducidad y acorta las claves anteriores a la gracia.
El nombre de una cuenta revocada **no se reutiliza** (las Apps que creó quedan a su nombre). Auditoría con actor `sa:<nombre>@platform` (el mismo que ven los avisos); rate limit de 600 peticiones/min por cuenta.

**Autenticación.** (a) Clave Ed25519 como las cuentas de App (`<nombre>@platform`); (b) **federación**: el job de GitLab canjea su ID token con `{"grant":"federated","platform":"<cuenta>"}` contra los vínculos *de esa cuenta*
(proyecto exacto, `ref_protected=true`, `environment` obligatorio, emisor permitido), sin secretos guardados. El token que emite plane lleva el claim firmado `plat` y **ningún grupo** (ni Kubernetes ni admin); no abre sesiones de Kubernetes.

**Autorización (server-side, en cada petición).** (1) `Authenticate` relee la cuenta del almacén (revocada/caducada = 401 al instante, aunque el token siga vigente). (2) `platformGate`: cada ruta declara su `scope` en la tabla de rutas;
sin scope declarado o sin que la cuenta lo tenga = 403 (default-deny; una ruta nueva nace cerrada). (3) El servicio vuelve a comprobar scope + App permitida contra los datos reales (dentro de `mutate`, no contra el token).

**`schedule:manage`** abre SOLO la programación de carga (`GET/PUT …/schedule`, override, carga medida) de las Apps permitidas. No incluye `apps:manage` ni lo incluye ningún otro scope, no abre escalado, cuota, red, miembros, cuentas ni crear/archivar Apps, y solo una persona administradora puede otorgarlo. Todo lo que puede hacer (réplicas y recursos por pod de los Deployments del entorno, dentro de la cuota) ya cabe en `apps:manage`, así que no amplía privilegios; lo que añade es un efecto persistente (el reconciliador aplica la programación cada minuto), por eso es un scope aparte. Pruebas de escalada en `acc_ola2_test.go` (`TestProgramacionConCuentaDePlataforma`) y en el servidor (`schedule_scope_test.go`).

**Qué recursos del provider operan con ella:** `bifrost_app` (con `owners`), `environment`, `service`, `service_account`(+`_key`), `member`, `binding`, `quota`, `network`, `host_rules`, `access`, `scaling`, `queue`, `runner_pool`,
`replication_link`, `schedule` (solo con `schedule:manage`) y los data sources `app`, `apps`, `environment`, `service`. **Siguen exigiendo persona administradora:** `bifrost_admin`, `bifrost_platform_account` (una cuenta no se crea a sí misma ni a otras: sería escalada),
`bifrost_person`, `restore_archived` y `on_destroy="purge"` (irreversibles/revierten decisiones), y las herramientas con opciones `admin_only` (p. ej. el nombre de la base).

### Revisión de seguridad adversarial (de este cambio)

| Riesgo | Hallazgo | Corrección / estado |
|---|---|---|
| Suplantación de la identidad de plataforma | Primera idea: reconocerla por el sufijo `@platform` del *subject*. Un job de CI controla el nombre de su rama, que va dentro del `sub` (`…ref:x@platform`). | La identidad sale **solo** del claim firmado `plat`; el sufijo del subject no concede nada (test `NoSePuedeFalsificar…`). |
| Escalada: crear/rotar/dar scopes | Ninguna ruta de gestión tiene scope; la compuerta las cierra aun con todos los scopes. | Test de default-deny sobre **todas** las rutas (`DefaultDenyPorRuta`) + intentos explícitos (admins, people, platform-accounts). |
| Herencia de Apps al reutilizar nombres | Si se reutilizara el nombre de una cuenta revocada, la nueva heredaría las Apps de la anterior (`managed_by`). | El nombre no se reutiliza, ni revocada. |
| Confused deputy entre Apps | `links` (pool/replication de una App), pool→App, enlace→App y `from` de un acceso aceptaban nombres ajenos. | Todas las referencias exigen App permitida / prefijo; `links` sin prefijo se rechaza. |
| Colas/pools sin dueño | Sin prefijo, `queues:manage` habría tocado las colas de todos los equipos. | `app_prefix` obligatorio con esos scopes; listas filtradas por prefijo; comparación literal (distingue mayúsculas). |
| Fuga por `apps:read` | `GET /apps/{n}` devuelve miembros, cuentas y vínculos. | Se redactan salvo `members:manage` / `identities:manage`. |
| Deshacer decisiones de un administrador | `restore` permitiría desarchivar lo que un administrador archivó. | `restore` solo administradores; `purge` jamás. |
| Agotamiento de recursos | Bucle de Terraform creando Apps; cuotas arbitrarias. | Tope 50 Apps/cuenta, cuota al crear exige `quota:manage`, rate limit por cuenta. |
| ReDoS / Unicode en `app_prefix` y nombres | Regex solo en la *validación* al crear (RE2, lineal, con longitud acotada antes); la comparación en caliente es `HasPrefix`. | Nombres/prefijos ASCII en minúsculas; el nombre de la App se normaliza antes de comparar (`Ｔienda-x` cae fuera, `Tienda-X` → `tienda-x` dentro). Tests. |
| TOCTOU scope↔ejecución | El grant se lee al inicio de la petición; revocar durante una petición larga deja terminar *esa* petición. La pertenencia a la App (inmutable: `managed_by`/lista/prefijo) se evalúa sobre la App leída dentro de `mutate`. | Ventana = una petición; la siguiente da 401. Documentado como riesgo residual aceptado. |
| Fuga de scopes/secretos en logs y auditoría | Los logs de acceso solo llevan el actor `sa:x@platform`; el error de la compuerta no lista scopes; la auditoría de creación lista scopes (dato de administración) y nunca claves. | Tests de que ni la respuesta del listado ni el token ni la auditoría contienen material privado. |
| Amplificación contra el almacén | Un canje no autenticado lee el ConfigMap de cuentas. | Ya limitado por IP (30/min) y por cuenta antes de verificar la firma; el grant de un token válido se relee con límite de 600 peticiones/min por cuenta. |

**Riesgos residuales / decisiones de producto:** (1) `identities:manage` (opt-in) equivale a acceso de despliegue a las Apps permitidas; (2) `members:manage` deja nombrar owners humanos de esas Apps, solo en `allowed_domains`; (3) el prefijo es obligatorio con `apps:create`;
(4) no hay aprobación de dos personas ni topes de caducidad/cantidad de identidades (decisión de producto, ver §7); (5) la clave privada queda en el state de Terraform salvo que se use la federación o el recurso efímero (§7).

## 7. Decisiones de seguridad y mitigaciones (segunda ronda)

**Decididas y aplicadas:** `apps:create` exige prefijo (también comprobado en el servidor al crear); `identities:manage` opt-in con límite; restaurar solo administradores; `allowed_domains` y límite de alcance en lo que crea la cuenta; auditoría + aviso prioritario por identidad; la pantalla del portal queda **pendiente de diseño en el lienzo** (no se tocó `web/`).
**Evaluadas y NO implementadas por decisión de producto:** tope de caducidad (30 días) de las identidades que crea la cuenta, aprobación de dos personas (estado pendiente) y límites por hora/totales de identidades y miembros. Se mantienen los topes que ya existían: 50 Apps por cuenta y 600 peticiones/min por cuenta.

**Clave privada en el state.** Ruta recomendada, en orden: (1) **federación sin secretos** (`federated_platform` / `federated_app`: el CI nunca guarda una clave); (2) **recurso efímero** `ephemeral "bifrost_service_account_credential"` (Terraform ≥ 1.10): la clave nueva no entra al state ni al plan y se
pasa a un atributo write-only de otro recurso; **abre y rota en cada plan/apply** (la anterior sigue valiendo solo la gracia del servidor), úsalo con vigencia corta (por omisión 1 día); (3) modo clásico: `key_json` sensible en el state, con backend cifrado en reposo y acceso restringido u OpenTofu state encryption, y `examples/.gitignore` para no subirlo a git.
`terraform plan` avisa cuando una clave va a quedar en el state. Los atributos write-only del framework (≥ 1.14) **no sirven para claves generadas por el servidor**: solo reciben valores de la configuración, no los devuelven el provider; por eso el efímero es la vía. `expires_in_days` (el «key_ttl») vale **7 por omisión** en `bifrost_service_account`/`_key` (valor por omisión configurable, no un tope).

**TOCTOU.** El grant sale del almacén al autenticar (revocada o caducada = 401 en la siguiente petición). Durante la petición: (a) cada efecto lateral llama a `liveCheck` justo antes (`mutate` antes de guardar, `provisionEnv`, adjuntar/quitar herramientas), que relee la cuenta (`platformsa.Directory.Alive`: no revocada y sin caducar);
(b) un **vigilante** (cada segundo) revalida mientras la petición está en curso y **cancela su contexto** si se revoca (reconcile, esperas). Pruebas: vigilante que cancela, efecto que no se escribe si la cuenta se revocó antes, y revocación concurrente bajo `-race`. Ventana residual: un efecto ya enviado al cluster no se deshace (es idempotente y se corrige con `reconcile`).

### Hallazgo de la revisión automática en `transport/httpapi/platform.go` (corregido)
Los handlers de gestión (`GET/POST /platform-accounts`, `…/{n}/keys`, `DELETE …/{n}`) se apoyaban solo en `roles: admin` de la ruta, en la compuerta de plataforma y en el booleano `admin` que reciben del servicio; no comprobaban de forma explícita la identidad dentro del handler ni validaban el nombre de la ruta.
Corrección: cada handler llama a `platformAdmin`, que exige **persona administradora autenticada** (`Platform == nil`, `!Machine`, `IsAdmin()`: solo identidad firmada, nunca claims del cuerpo ni parámetros del cliente) y valida `{name}` con `platformsa.ValidName` (404 si no es un nombre válido: sin IDOR por nombres raros, normalización ni traversal). El actor de auditoría sale siempre de la identidad.
Verificado con tests de regresión: ninguna de las 4 rutas es alcanzable por cuenta de plataforma, cuenta de App, persona sin rol de administrador, sin token o con token basura (los 403/401 no filtran claves ni scopes); no existe ruta de aprobación; nombres inválidos dan 404; el listado nunca incluye claves privadas; la auditoría nunca contiene material de claves.

### Revisión adversarial breve de esta ronda
| Riesgo | Resultado |
|---|---|
| Owner = otra cuenta de plataforma o dominio externo | Rechazado (`DomainOK`: dominios permitidos, nunca `sa:`/`ci:`/`@platform`); probado con mayúsculas, subdominios y `x.com.evil.com`. |
| Rotar/renovar una cuenta que ya no cumple las reglas | `Rotate` la rechaza (se recrea); `Grant` le quita los scopes no permitidos en cada petición. |
| Alcance mayor que el propio al crear identidades | Cuentas de servicio sin entornos explícitos y vínculos laxos se rechazan; los vínculos no se crean con comodines. |
| Aviso spoofeable | El evento `platform.identity` solo usa valores de lista cerrada; el detalle crudo nunca se copia. |
| Vigilante / `Alive` como amplificación contra el almacén | Una lectura por segundo por petición de plataforma en curso, limitada a 600 peticiones/min por cuenta. |
| Efímero rota en cada plan | Documentado; no se usa en recursos que lean la clave en `plan` sin querer. |

### Revisión de seguridad sobre main (merge 4a78c1c): `handlers_apps.go` y `platform.go`
Revisión adversarial de ambos archivos con una matriz de regresión (`platform3_test.go`: cada ruta con scope × App ajena con 8 variantes de nombre × 3 formas de consulta con `purge`/`confirm`/`from`, más las rutas hijas sin scope).
**`handlers_apps.go` — hallazgos reales y correcciones**
1. *Rutas con scope que fallaban cerradas por error*: escalar (`PUT …/scaling`) y leer la conexión de una herramienta pasaban por comprobaciones «puede desplegar» (`scaleEnv`/`deployEnv`) que no conocían a la cuenta de plataforma, así que `bifrost_scaling` y `data.bifrost_service` no funcionaban con ella. Ahora se permiten **solo** con su scope (`apps:manage` / `apps:read`), sobre Apps permitidas, y el scope no se «presta» a otra operación (la ruta debe ser la que declara ese scope).
2. *Nombres imposibles llegaban al cluster* (`%00`, saltos de línea, mayúsculas…): `KubeStore.Get` devolvía 502 al construir el nombre del ConfigMap. Ahora un nombre que no puede ser de una App es 404 sin tocar el cluster.
3. *`DELETE …/access?from=`*: una cuenta con `network:manage` podía quitar reglas cuyo origen es una App ajena (p. ej. puestas por un administrador). Ahora el origen debe ser permitido (o no existir ya).
4. *Fuga de nombres por conflicto de dominio*: `SetNetwork` respondía «ya lo usa <app>/<entorno>» a cualquiera; ahora solo a administradores.
5. *Fuga en `apps:read`*: además de miembros/cuentas/vínculos, ya no salen los correos de quien creó la App, el entorno o añadió una herramienta (`created_by`, `added_by`) sin `members:manage`.
6. *Orden de comprobaciones*: la disponibilidad de las reglas de host se consultaba antes de comprobar permisos (sondeo de Apps por un 503 distinto); ahora va después.
Verificado sin hallazgo: el scope se comprueba en la compuerta y la pertenencia (creada/lista/prefijo) dentro del servicio sobre la App leída, por el **nombre normalizado** para crear y por **nombre exacto almacenado** para leer/modificar (una variante de mayúsculas, espacios o `%73` no existe: 404); purge y restore son solo administradores incluso con `?purge=true&confirm=`; rutas de logs, métricas, eventos, historial, pods, restart y reapply no tienen scope (cerradas); los listados se filtran por la lista/prefijo; crear con un nombre que normaliza a una App ajena fuera del prefijo es 403, y dentro del prefijo contra una archivada es 409 sin restaurar.
**`platform.go` / `platformsa` — hallazgos y correcciones**
- *Comparación de correos*: `DomainOK` ahora exige una sola `@`, parte local no vacía, sin espacios ni separadores (`,;<>"`), dominio ASCII e **igualdad** con la lista (rechaza `a.evil.com`, `evil.com.x.com`, punto final, homoglifos cirílicos o de ancho completo, `a@evil.com@x.com`, `sa:`/`ci:`/`@platform`).
- *Degradación por falta de prefijo/opt-in/dominios*: se aplica en un único punto (`Directory.Grant`, usado por `Authenticate` en cada petición), por eso rige en TODAS las rutas; probado en crear, miembros, cuentas, vínculos y lecturas que conserva.
- *Rotación*: el cuerpo no puede traer scopes, prefijo, apps ni dominios (se ignoran); una cuenta anterior que ya no cumple no se renueva.
- *`owners`*: no acepta cuentas de plataforma ni correos fuera de los dominios; **nombrar a un administrador como owner no se bloquea**: un administrador ya es owner implícito de toda App, no concede nada.
- *`/platform-accounts`*: sigue inalcanzable para cuentas de plataforma, de App y personas sin rol de administrador (regresión en `platform2_test.go`); nada depende de claims sin firmar (la identidad sale del claim `plat` firmado y se relee del almacén).
- *Prueba flaky corregida*: la de revocación concurrente contaba como fallo una petición iniciada antes de revocar.

### Fail-closed de `access.go` (hallazgo del commit 0995c4a) e importación sin reemplazos
- `DELETE …/access?from=` seguía a ciegas si **leer** el origen fallaba (la comprobación solo actuaba cuando la lectura salía bien): un error o timeout del almacén dejaba pasar a la cuenta de plataforma. Ahora se decide por **conjunto permitido**: el origen se lee y debe ser permitido, o la lectura dice explícitamente `ErrNotFound` (limpieza de una regla cuyo origen ya no existe, y solo si la regla existe en el destino); cualquier otro error devuelve 502 sin quitar nada; un dato inesperado (nombre distinto del pedido) deniega; identidades sin administrador ni cuenta de plataforma reciben 403. `POST` aplica lo mismo (origen ilegible = 502, ajeno/archivado = 400).
  Además `PlatformGrant.Permits` ya no concede por valor cero (un grant sin nombre coincidía con las Apps creadas por personas, que tienen `managed_by` vacío).
- Importación: `bifrost_service_account` importada quedaba con `expires_in_days = null` y el esquema tenía `Default: 7` + `RequiresReplace`, así que el primer plan la **reemplazaba** (revocando su clave). Ahora se importa con el valor por omisión y `expires_in_days` solo fuerza reemplazo si Terraform creó la cuenta (hay clave en el state); en una importada es un cambio solo de estado. `bifrost_platform_account` importada no rota la clave al declarar `expires_in_days`/`keepers` por primera vez. Test `import → plan` por recurso (app, environment, service, service_account, member, binding, quota, network, host_rules, access, queue, runner_pool, admin): 0 cambios y 0 reemplazos; el smoke repite la comprobación con un estado nuevo. Los atributos de protección/comportamiento (`confirm_data_loss`, `adopt_existing`) se declaran después como cambio de estado.

### Rotación de la clave de `bifrost_platform_account` (hallazgo fail-open-credential-rotation)
El criterio «importar ya no rota al declarar `expires_in_days`/`keepers` por primera vez» dejaba pasar casos en que **no se rotaba pese a un cambio de keepers o una clave inservible** (nulos tras importar se tomaban como «adopción»). Ahora el criterio (`shouldRotate`) **falla cerrado hacia rotar**:
- **keepers**: una cuenta importada nace con el centinela `keepers = { imported = "true" }`; cualquier `keepers` declarado es un valor distinto, así que **siempre rota** (también el primero tras importar); sin declararlo no cambia nada. Si cambia después, rota. Un valor desconocido rota.
- **expires_in_days**: distinto de lo guardado, rota. En una cuenta importada se compara con los **días efectivos** que le quedan (la API devuelve `expires_at`): igual (±1 día) = adopción solo de estado, sin rotar ni afirmar que se rotó; distinto, o caducidad ilegible, rota.
- **Clave vencida, por vencer (< 72 h), revocada, inexistente o ilegible**: nuevo atributo `key_expires_at` (la vigencia más tardía entre las claves no revocadas, leída en cada refresh). Si es nulo, vacío, ilegible, vencido o < 72 h, o la cuenta vence en < 72 h, el **plan avisa y fuerza una actualización que rota**, sin que el usuario cambie nada; una importación con la clave en ese estado también avisa.
- Un error al leer la cuenta/claves es un error del refresh (nunca «no rotar»).
- Tras rotar, la clave anterior queda con la **gracia del servidor (1 h por omisión)** y luego deja de valer; esa es la única ventana con dos claves vigentes. Los avisos solo mencionan el motivo, nunca la clave (que es sensible en el state).
Tests: criterio unitario con 15 casos negativos, import→declarar keepers (con y sin cambio de valor), import→expires efectivo / distinto, clave vencida y revocada por fuera, y que solo la última clave valga.
