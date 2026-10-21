# Inventario de la API de Bifröst usada por el provider

Fuente: `internal/transport/httpapi` (tabla `routes()`), `internal/apps`, `queues`, `runners`, `replication`, `admins`, `people`, `plane`.
Leído del código, no supuesto. Fecha: 2026-10-19. Todas las rutas se escriben sin el prefijo `/v1` (plane) o `/api` (portal).

## Cómo se entra

- El portal (`/api/*`) va detrás de Cloudflare Access / sesión de navegador: **no sirve para máquinas**. Las máquinas y la CLI usan **plane**
  (`https://plane.<dominio>`), que espeja *las mismas rutas* bajo `/v1/*` (`/api/apps/x` -> `/v1/apps/x`) con los mismos handlers.
- Descubrimiento: `GET /.well-known/bifrost` -> `{issuer, api, token_uri, sessions_uri, oidc{...}, version}`. `api` es la base de `/v1`.
- Canje de identidad: `POST /auth/token` (público, límite de intentos por IP) con `{grant, assertion|id_token, app}` -> `{access_token, token_type:"Bearer", expires_in}` (~1 h).
  - `grant=service_account`: `assertion` = JWT EdDSA (kid = `private_key_id`, iss = sub = `account`, aud = `token_uri`, exp <= 5 min, `jti` único).
  - `grant=federated`: `id_token` del CI (GitLab) + `app` opcional.
  - `grant=user`: `id_token` del IdP de personas (OIDC); el correo debe venir verificado. Si la persona es administradora el token lleva el grupo admin.
- Todas las rutas `/v1/*` exigen `Authorization: Bearer <token>`.
- **Restricción importante** (`machineAllowed`, `IsAdmin`): una identidad de máquina de una App (`sa:…@app-x` cuenta de servicio, `ci:` vínculo federado) **nunca es
  administradora** y solo alcanza `/v1/apps/{su-app}/envs/{env-permitido}/...`, `/v1/me`, `/v1/services*` y `POST /v1/sessions`.
- **Cuenta de servicio de plataforma** (nueva): identidad de máquina `sa:<nombre>@platform` con **scopes** (claim firmado `plat`; grant releído del almacén en cada petición). No es administradora.
  Canje: `grant=service_account` con `iss=<nombre>@platform`, o `grant=federated` + `platform=<nombre>` (vínculos de la cuenta: proyecto exacto, `ref_protected`, `environment`). Tabla ruta → scope más abajo.
  Gestión (solo persona administradora): `POST/GET /platform-accounts`, `POST /platform-accounts/{n}/keys`, `DELETE /platform-accounts/{n}`.

## Errores

Cuerpo `{"error":"mensaje en español"}`. Códigos: 400 inválido, 401 sin/mal token, 403 prohibido, 404 no existe, 409 conflicto, 429 límite,
502 falla del cluster/dependencia, 503 no disponible/no habilitado, 500 interno. Los 5xx llevan mensaje seguro (la causa va solo al log del servidor).

## Convenciones de ciclo de vida

- **Aprovisionamiento síncrono**: `POST /apps`, `POST .../envs`, `reconcile`, `restore` aprovisionan *dentro* de la petición; al volver el entorno ya
  está `active` o `error`. Un fallo devuelve 502 **pero la App/entorno queda creada** en estado `error` (reintentable con `reconcile`). No hay
  cola asíncrona: basta comprobar `status` (el provider reintenta `reconcile` y espera hasta el timeout).
- **Borrado en dos fases** (App y entorno): `DELETE` archiva (idempotente; sobre algo ya archivado devuelve 200). `DELETE ?purge=true&confirm=<nombre>`
  elimina de verdad y solo si: está archivado, pasó la retención (`RetentionDays` del servidor; 409 con fecha si no), no quedan herramientas
  en la App/entorno, y (entorno) no es el último entorno ni hay cuentas/vínculos limitados solo a él. Purgar un entorno exige administrador.
- **Herramientas**: `DELETE .../services/{id}?confirm=<id>` **borra los datos**. Las opciones de una herramienta ya añadida **no se cambian**.
- **Sin ETag/If-Match**; el servidor serializa con guardado optimista interno (409 "cambió mientras guardabas" -> reintentar).

Rol: `adm` = solo administrador; `own` = administrador u owner de la App; `any` = miembro con lectura.

## Apps y entornos

| Método y ruta | Rol | Cuerpo | Respuesta / notas |
|---|---|---|---|
| `POST /apps` | adm | `{name, description, owner, quota{cpu,memory,pods,pvcs}, envs[{name,quota}]}` | 201 `App`. `name` se normaliza a slug (2-30, empieza con letra, no reservado: system default infra kube bifrost plane). Sin `envs` crea `prod`. Máx 8 entornos. `owner` por omisión = quien crea. Síncrono. 409 si existe |
| `GET /apps/{name}` | any | | 200 `App` (envs, accounts, bindings, members, links, quota). 404. **Única lectura de cuentas, vínculos y miembros** |
| `GET /apps` | any | | `{items:[App]}` |
| `PATCH /apps/{name}` | own | `{display_name?, description?}` | 200 `App`. 80 / 200 chars sin control. Archivada: 409 |
| `PUT /apps/{name}/quota` | adm | `{cpu,memory,pods,pvcs}` | 200 `App`. **Solo la cuota por omisión de entornos NUEVOS.** cpu "2"/"500m", memory "4Gi", pods 1-500, pvcs 0-100 |
| `PUT /apps/{name}/links` | own | `{pool:string\|null, replication:[string]}` | 200 `App`. Máx 16 enlaces |
| `DELETE /apps/{name}` | adm | `?purge=true&confirm=<name>` | archivar: 200 `App`; purgar: 200 `{ok}` |
| `POST /apps/{name}/restore`, `/reconcile` | adm | | 200 `App` |
| `POST /apps/{n}/envs` | own | `{name, quota?}` | 201 `AppEnv`. slug 2-16. Sin `quota` = la de la App. Namespace `app-<app>-<env>` |
| `GET /apps/{n}/envs/{e}` | any | | 200 `AppEnv` (status, error, quota, hosts, services, network). 404 |
| `DELETE /apps/{n}/envs/{e}` | own (purge: adm) | `?purge&confirm=<env>` | archiva / elimina |
| `POST /apps/{n}/envs/{e}/restore`, `/reconcile` | own | | 200 `AppEnv` |
| (no existe) cambiar la cuota de un entorno | | | **Brecha #1** |

## Red, reglas por host y acceso

| Método y ruta | Rol | Cuerpo | Respuesta / notas |
|---|---|---|---|
| `PUT /apps/{n}/envs/{e}/network` | adm | `{domains[], internet, internet_ports[], egress?[{cidr,port,reason}]}` | 200 `AppEnv`. **Reemplaza la red entera**; `egress` ausente = conserva, presente (aun `[]`) = reemplaza. Máx 10 dominios (hostname completo, sin comodines/IP, no de la plataforma, no usado por otro entorno -> 409), máx 10 puertos 1-65535, puertos requieren `internet=true`, `egress` e `internet` excluyentes. Se guarda y luego se aplica: si el cluster falla, 502 y lo deseado queda guardado (usar reconcile) |
| lectura de vuelta | any | `GET /apps/{n}/envs/{e}` -> `network{domains,internet,internet_ports,egress}` | `internet_ports` efectivos incluyen 443,80 por omisión con internet; sin internet es `[]` |
| `GET /apps/{n}/envs/{e}/hosts/{host}/rules` | any | | `{host, rules{ip_allow, ip_deny, ip_notes, rate_limit, basic_auth[{name}], www, security_headers, compress, max_body_mb, cors}, routes, updated_by, updated_at}`. **Nunca devuelve contraseñas** |
| `PUT /apps/{n}/envs/{e}/hosts/{host}/rules` | adm | mismo `rules` con `basic_auth[{name,password}]` | Reemplaza todo. Idempotente. No hay drift de contraseña |
| `…/envs/{e}/certificates`, `…/hosts/{host}/certificate[/history\|/chain\|/retry]` | personas | ver `bifrost/docs/certificados.md` | Sin scope de plataforma (default-deny): una cuenta de servicio no los lee ni sube claves privadas. Solo administradores y owners suben/quitan/reintentan |
| `GET /apps/{n}/envs/{e}/access` | any | | `{incoming[{app,service,port}], outgoing[...]}` (reglas *entrantes* de `{n}/{e}`) |
| `POST /apps/{n}/envs/{e}/access` | adm | `{from, service, port}` | 201 lista. 409 si ya existe; 400 si `from` no existe, no tiene ese entorno o el servicio no expone el puerto |
| `DELETE /apps/{n}/envs/{e}/access?from=&service=&port=` | adm | | 200 lista |

## Escalado

| Método y ruta | Rol | Cuerpo | Respuesta / notas |
|---|---|---|---|
| `GET /apps/{n}/envs/{e}/scaling` | any | | `{workloads[{kind,name,replicas,request,limit,hpa{min,max,cpu_target,memory_target,managed},vertical{mode,min,max}}], quota, vertical_available}` |
| `PUT /apps/{n}/envs/{e}/scaling/{kind}/{workload}` | own | `{horizontal{mode:fixed\|auto,replicas,min,max,cpu_target,memory_target}?, vertical{mode:off\|recommend\|auto,min_cpu_m,max_cpu_m,min_memory_bytes,max_memory_bytes}?}` | `{ok}`. `kind` Deployment\|StatefulSet (StatefulSet sin horizontal). Debe caber en la cuota. **Falla si la carga no existe** (la crea el despliegue del usuario). No hay "quitar" el escalado, solo reescribirlo |

## Herramientas (servicios)

| Método y ruta | Rol | Cuerpo | Respuesta / notas |
|---|---|---|---|
| `GET /services`, `GET /services/{id}` | any | | catálogo: `id,name,isolation,dedicated_support,secret_name,options[{key,type,default,enum,admin_only}]` |
| `POST /apps/{n}/envs/{e}/services` | own | `{service, options{k:v}, dedicated}` | 201 `{service,secret_name,dedicated,resources[],added_by,added_at}`. 409 si ya está; entorno `active`; opciones `admin_only` requieren admin. Si falla abrir la red: 502 pero queda añadida |
| `GET /apps/{n}/envs/{e}/services[/{id}]` | any | | lista / uso. **No devuelve las opciones con las que se creó** (limitación para detectar cambio de opciones) |
| `GET .../services/{id}/connection` | any | | `{secret_name, keys[{key, secret, value}]}`: las `secret:true` **van sin valor**; solo las no sensibles traen valor |
| `POST .../services/{id}/reapply` | own | | |
| `DELETE .../services/{id}?confirm=<id>` | own | | **Borra los datos** |

## Cuentas de servicio, vínculos, miembros

| Método y ruta | Rol | Cuerpo | Respuesta / notas |
|---|---|---|---|
| `POST /apps/{n}/accounts` | own | `{name, role:deploy\|readonly, expires_in_days, envs[]}` | 201 `{account, key_file{type,api,account,private_key_id,private_key}}`. **La clave privada solo viaja aquí.** Vacío `envs` = todos. 409 si el nombre existe, **incluso revocada** (brecha #2) |
| `POST /apps/{n}/accounts/{a}/keys` | own | `{expires_in_days?}` | 201 `{key_file}`. Rota: añade clave, acorta las anteriores a `RotateGrace` |
| `DELETE /apps/{n}/accounts/{a}` | own | | **Revoca** (sigue listada con `revoked=true`) |
| `GET /apps/{n}/accounts` | own | | `{items[{name,id,role,envs,revoked,keys[{id,created,expires,revoked}]}]}`. Sin PATCH: rol y entornos inmutables |
| `POST /apps/{n}/bindings` | own | `{kind:"gitlab", issuer, project_path, ref_protected, claims{}, role, envs[]}` | 201 `{id,...}`. `issuer` debe estar permitido (400); `project_path` `grupo/repo` o `grupo/*`; (issuer, project_path) único en toda la plataforma (409); máx 8 claims |
| `GET /apps/{n}/bindings` | own | | lista. Sin PATCH; el `id` lo asigna el servidor |
| `DELETE /apps/{n}/bindings/{id}` | own | | |
| `POST /apps/{n}/members` | own | `{email, role:owner\|deploy\|readonly}` | 201. 409 si ya es miembro; máx 50 |
| `PUT /apps/{n}/members/{email}` | own | `{role}` | 200. El último owner no se degrada (409) |
| `DELETE /apps/{n}/members/{email}` | own | | El último owner no se quita (409) |
| `GET /apps/{n}/members` | miembro | | `{items[{email,role,added_by,added_at}]}` (no máquinas) |

## Colas, runners, replicación, administradores y personas

| Método y ruta | Rol | Cuerpo | Respuesta / notas |
|---|---|---|---|
| `GET /queues` | any | | `{items[{kind,name,display,registered,discovered,hidden,counts,jetstream{...}}], ...}`. **Sin GET por nombre**: se busca (kind,name) en la lista |
| `POST /queues` | adm | `{name, display, kind:bullmq\|jetstream, jetstream{subjects,retention:workqueue\|limits,replicas:1\|3,storage:file\|memory,max_age(s),max_msgs,max_bytes}}` | 201. Nombre <=80, `[A-Za-z0-9][A-Za-z0-9_.-]*`. 409 si ya declarada. El stream es inmutable |
| `PUT /queues/{name}?kind=` | adm | `{display?, hidden?}` | Solo esos dos campos son editables |
| `DELETE /queues/{name}?kind=&confirm=<name>` | adm | | JetStream: **borra stream, DLQ y consumidores** |
| `GET /runners` | any | | `{pools[{name,labels,scope,min,max,origins,paused,app,pending,observed}],...}`. Sin GET por nombre |
| `POST /runners/pools` | adm | `{name,labels,scope:project\|group\|shared,min,max,origins,app}` | 201. Nombre `[a-z0-9][a-z0-9-]{0,39}`, <=10 etiquetas, min >=0, max 1-100, `origins` >=1, `app` debe existir |
| `PUT /runners/pools/{name}` | adm | `{paused?,app?,origins?,min?,max?}` | `labels` y `scope` inmutables |
| `DELETE /runners/pools/{name}?confirm=<name>` | adm | | |
| `POST /replication/links` | adm | `{name, source, destination, mode:one_way\|two_way, app}` | **202** `{link,version,state,native_url}`. Solo *propone*: queda pendiente de aprobación en Ratatoskr; las conexiones deben existir allí. 409 si ya existe; 503 si no hay token publisher |
| `GET /replication/links/{name}` | any | | `LinkDetail` (404 hasta que exista/se apruebe) |
| (no existe) borrar/editar un enlace | | | **Brecha #3** |
| `GET /admins` / `POST /admins {email}` / `DELETE /admins/{email}` | adm | | `{email,source:bootstrap\|bifrost,added_by,added_at}`. Los `bootstrap` no se quitan (400); no se deja sin administradores (400); 404 si no lo es |
| `GET /people[/{email}]` | adm | | `{email,admin,admin_source,apps[{app,role}]}` |
| `DELETE /people/{email}` | adm | | "Olvida" a una persona sin permisos; 409 si tiene roles o es admin |
| (no existe) `POST /people` | | | **Brecha #4** |

Otras (no se modelan como recursos): `GET /me` (el provider lo usa para validar credenciales), `/status`, `/config`, `/keys`, `/tunnels`, `/backups`, `/audit`, `/notifications*`, `/hooks/*`.

## Brechas de API

1. **Cuota de un entorno no editable**: no hay ruta para cambiar la `quota` de un entorno existente; `PUT /apps/{n}/quota` solo fija el valor por omisión de entornos *nuevos*. `bifrost_environment.quota` es inmutable (cambiarla se rechaza con un error que explica la salida); `bifrost_quota` administra la cuota por omisión de la App.
2. **Recrear una cuenta de servicio con el mismo nombre falla** tras revocarla (la revocada sigue en la lista). Rol y `envs` no se pueden cambiar -> cambiarlos fuerza reemplazo y hay que cambiar también el `name`.
3. **Enlaces de replicación sin borrado ni edición**: `bifrost_replication_link` solo propone; `destroy` solo lo quita del state.
4. **No se pueden crear personas**: `bifrost_person` es data source (y su "olvidar" no está modelado como recurso).
5. **Las máquinas no administran la plataforma**: ni cuentas de servicio ni vínculos federados llegan a rutas `adm`. La IaC de plataforma exige una persona administradora (`grant=user` con ID token, o token directo). Falta una "cuenta de servicio de plataforma".
6. Sin `DELETE` de claves de cuenta (solo rotar o revocar la cuenta), sin GET por nombre de colas/pools/enlaces (se lee de listas), sin forma de quitar el escalado de una carga, y `GET services` no devuelve las opciones de creación.

## Cuentas de servicio de plataforma: scope por ruta

Default-deny: toda ruta que no figura aquí está **cerrada** (403) para ellas (incluye `/admins`, `/people`, `/platform-accounts`, `/keys`, `/tunnels`, `/backups`, `/audit`, `/notifications`, `/status`, `/config`, y los purge/restore).
Reglas: `apps:create` exige prefijo; `identities:manage` es opt-in con límite; `apps:create`/`members:manage` exigen `allowed_domains`; las identidades creadas llevan entornos explícitos (y vínculos estrictos). Además del scope, la App debe ser *permitida* (creada por la cuenta, en su lista o dentro de su `app_prefix`; si no, 404). Colas, pools y enlaces: nombre con el `app_prefix` (obligatorio para esos scopes).

| Scope | Rutas |
|---|---|
| `apps:read` | `GET /apps`, `GET /apps/{n}`, `GET …/envs`, `…/envs/{e}`, `…/access`, `…/scaling`, `…/services`, `…/services/{id}/connection`, `…/hosts/{h}/rules`, `GET /services[/{id}]` (miembros/cuentas/vínculos del cuerpo de la App se redactan sin `members:manage`/`identities:manage`) |
| `apps:create` | `POST /apps` (con `owners` obligatorio; `quota` exige además `quota:manage`) |
| `apps:manage` | (restaurar NO: solo admin) `PATCH/DELETE (archivar) /apps/{n}`, `PUT …/links`, `POST …/reconcile`, `POST/DELETE …/envs[/{e}]`, `…/envs/{e}/reconcile`, `PUT …/scaling/{k}/{w}`, `POST/DELETE …/services` |
| `quota:manage` | `PUT /apps/{n}/quota`; cuota explícita en `POST …/envs` |
| `network:manage` | `PUT …/network`, `PUT …/hosts/{h}/rules`, `POST/DELETE …/access` (origen y destino permitidos) |
| `members:manage` | `GET/POST/PUT/DELETE …/members` |
| `identities:manage` | `GET/POST/DELETE …/accounts`, `…/accounts/{a}/keys`, `GET/POST/DELETE …/bindings` |
| `queues:read|manage` | `GET /queues` (filtrada por prefijo) · `POST/PUT/DELETE /queues` (nombre con prefijo) |
| `runners:read|manage` | `GET /runners` (solo sus pools) · `POST/PUT/DELETE /runners/pools` (nombre con prefijo; `app` permitida) |
| `ports:manage` | `GET/PUT /apps/{a}/envs/{e}/egress` · `DELETE /apps/{a}/envs/{e}/egress/{id}` (salidas por workload; mismo scope y opt-in) · `GET/PUT /apps/{a}/envs/{e}/ports` · `DELETE /apps/{a}/envs/{e}/ports/{id}` (solo Apps permitidas; opt-in `allow_ports`) |
| `replication:read|propose` | `GET /replication/links/{n}` · `POST /replication/links` (nombre con prefijo; `app` permitida) |

Recursos del provider **operables** con una cuenta de plataforma: `app`, `environment`, `service`, `service_account`(+`_key`), `member`, `binding`, `quota`, `network`, `host_rules`, `access`, `scaling`, `schedule` (con el scope `schedule:manage`), `ports` (con el scope `ports:manage`), `queue`, `runner_pool`, `replication_link`
y los data sources `app`, `apps`, `environment`, `service`. **Exigen persona administradora:** `admin`, `platform_account`, `person` (data source), `on_destroy = "purge"`, `restore_archived`, opciones de herramientas `admin_only`.

Brecha #5 (antes: "las máquinas no administran la plataforma") queda **resuelta por diseño** con esta cuenta: sigue sin existir un administrador-máquina.
