package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

func esc(s string) string { return url.PathEscape(s) }

func appPath(app string) string { return "/apps/" + esc(app) }

func envPath(app, env string) string { return appPath(app) + "/envs/" + esc(env) }

// ---- identidad ----

func (c *Client) Me(ctx context.Context) (Me, error) {
	var m Me
	err := c.Call(ctx, http.MethodGet, "/me", nil, nil, &m)
	return m, err
}

// ---- Apps ----

func (c *Client) CreateApp(ctx context.Context, r CreateAppReq) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodPost, "/apps", nil, r, &a)
	return a, err
}

func (c *Client) GetApp(ctx context.Context, name string) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodGet, appPath(name), nil, nil, &a)
	return a, err
}

func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	var l struct {
		Items []App `json:"items"`
	}
	err := c.Call(ctx, http.MethodGet, "/apps", nil, nil, &l)
	return l.Items, err
}

func (c *Client) UpdateApp(ctx context.Context, name string, r SettingsReq) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodPatch, appPath(name), nil, r, &a)
	return a, err
}

func (c *Client) SetQuota(ctx context.Context, name string, q Quota) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodPut, appPath(name)+"/quota", nil, q, &a)
	return a, err
}

// ArchiveApp archiva la App (DELETE sin purge). Es idempotente.
func (c *Client) ArchiveApp(ctx context.Context, name string) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodDelete, appPath(name), nil, nil, &a)
	return a, err
}

// PurgeApp elimina una App ya archivada y fuera de retención.
func (c *Client) PurgeApp(ctx context.Context, name string) error {
	return c.Call(ctx, http.MethodDelete, appPath(name), url.Values{"purge": {"true"}, "confirm": {name}}, nil, nil)
}

func (c *Client) RestoreApp(ctx context.Context, name string) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodPost, appPath(name)+"/restore", nil, nil, &a)
	return a, err
}

func (c *Client) ReconcileApp(ctx context.Context, name string) (App, error) {
	var a App
	err := c.Call(ctx, http.MethodPost, appPath(name)+"/reconcile", nil, nil, &a)
	return a, err
}

// ---- entornos ----

func (c *Client) CreateEnv(ctx context.Context, app string, r CreateEnvReq) (Env, error) {
	var e Env
	err := c.Call(ctx, http.MethodPost, appPath(app)+"/envs", nil, r, &e)
	return e, err
}

func (c *Client) GetEnv(ctx context.Context, app, env string) (Env, error) {
	var e Env
	err := c.Call(ctx, http.MethodGet, envPath(app, env), nil, nil, &e)
	return e, err
}

func (c *Client) ArchiveEnv(ctx context.Context, app, env string) (Env, error) {
	var e Env
	err := c.Call(ctx, http.MethodDelete, envPath(app, env), nil, nil, &e)
	return e, err
}

func (c *Client) PurgeEnv(ctx context.Context, app, env string) error {
	return c.Call(ctx, http.MethodDelete, envPath(app, env), url.Values{"purge": {"true"}, "confirm": {env}}, nil, nil)
}

func (c *Client) RestoreEnv(ctx context.Context, app, env string) (Env, error) {
	var e Env
	err := c.Call(ctx, http.MethodPost, envPath(app, env)+"/restore", nil, nil, &e)
	return e, err
}

func (c *Client) ReconcileEnv(ctx context.Context, app, env string) (Env, error) {
	var e Env
	err := c.Call(ctx, http.MethodPost, envPath(app, env)+"/reconcile", nil, nil, &e)
	return e, err
}

func (c *Client) SetNetwork(ctx context.Context, app, env string, r NetworkReq) (Env, error) {
	var e Env
	err := c.Call(ctx, http.MethodPut, envPath(app, env)+"/network", nil, r, &e)
	return e, err
}

// ---- reglas por host ----

func hostPath(app, env, host string) string {
	return envPath(app, env) + "/hosts/" + esc(host) + "/rules"
}

func (c *Client) GetHostRules(ctx context.Context, app, env, host string) (HostRulesView, error) {
	var v HostRulesView
	err := c.Call(ctx, http.MethodGet, hostPath(app, env, host), nil, nil, &v)
	return v, err
}

func (c *Client) SetHostRules(ctx context.Context, app, env, host string, r HostRules) (HostRulesView, error) {
	var v HostRulesView
	err := c.Call(ctx, http.MethodPut, hostPath(app, env, host), nil, r, &v)
	return v, err
}

// ---- programación de carga ----

func schedulePath(app, env string) string { return envPath(app, env) + "/schedule" }

func (c *Client) GetSchedule(ctx context.Context, app, env string) (ScheduleView, error) {
	var v ScheduleView
	err := c.Call(ctx, http.MethodGet, schedulePath(app, env), nil, nil, &v)
	return v, err
}

// SetSchedule REEMPLAZA perfiles, horario semanal, excepciones, zona y `enabled`. No envía `version`: Terraform manda lo declarado.
func (c *Client) SetSchedule(ctx context.Context, app, env string, s ScheduleSpec) (ScheduleView, error) {
	var v ScheduleView
	err := c.Call(ctx, http.MethodPut, schedulePath(app, env), nil, s, &v)
	return v, err
}

// ---- puertos públicos TCP/UDP ----

func portsPath(app, env string) string { return envPath(app, env) + "/ports" }

func (c *Client) GetPorts(ctx context.Context, app, env string) (PortsView, error) {
	var v PortsView
	err := c.Call(ctx, http.MethodGet, portsPath(app, env), nil, nil, &v)
	return v, err
}

// SetPorts REEMPLAZA la lista completa de puertos del entorno. 409 si la versión cambió o si un puerto choca con otro entorno u otra App.
func (c *Client) SetPorts(ctx context.Context, app, env string, r SetPortsReq) (PortsView, error) {
	var v PortsView
	err := c.Call(ctx, http.MethodPut, portsPath(app, env), nil, r, &v)
	return v, err
}

// ---- salidas a internet por workload ----

func egressPath(app, env string) string { return envPath(app, env) + "/egress" }

func (c *Client) GetEgress(ctx context.Context, app, env string) (EgressView, error) {
	var v EgressView
	err := c.Call(ctx, http.MethodGet, egressPath(app, env), nil, nil, &v)
	return v, err
}

// SetEgress REEMPLAZA la lista completa de reglas de salida del entorno. 409 si la versión cambió.
func (c *Client) SetEgress(ctx context.Context, app, env string, r SetEgressReq) (EgressView, error) {
	var v EgressView
	err := c.Call(ctx, http.MethodPut, egressPath(app, env), nil, r, &v)
	return v, err
}

// ---- acceso entre Apps ----

func (c *Client) ListAccess(ctx context.Context, app, env string) (Access, error) {
	var a Access
	err := c.Call(ctx, http.MethodGet, envPath(app, env)+"/access", nil, nil, &a)
	return a, err
}

func (c *Client) AllowAccess(ctx context.Context, app, env string, r AllowAccessReq) error {
	return c.Call(ctx, http.MethodPost, envPath(app, env)+"/access", nil, r, nil)
}

func (c *Client) RevokeAccess(ctx context.Context, app, env, from, service string, port int64) error {
	q := url.Values{"from": {from}, "service": {service}, "port": {strconv.FormatInt(port, 10)}}
	return c.Call(ctx, http.MethodDelete, envPath(app, env)+"/access", q, nil, nil)
}

// ---- escalado ----

func (c *Client) GetScaling(ctx context.Context, app, env string) (Scaling, error) {
	var s Scaling
	err := c.Call(ctx, http.MethodGet, envPath(app, env)+"/scaling", nil, nil, &s)
	return s, err
}

func (c *Client) SetScaling(ctx context.Context, app, env, kind, workload string, r SetScalingReq) error {
	return c.Call(ctx, http.MethodPut, envPath(app, env)+"/scaling/"+esc(kind)+"/"+esc(workload), nil, r, nil)
}

// ---- herramientas ----

func (c *Client) AttachService(ctx context.Context, app, env string, r AttachServiceReq) (Service, error) {
	var s Service
	err := c.Call(ctx, http.MethodPost, envPath(app, env)+"/services", nil, r, &s)
	return s, err
}

func (c *Client) DetachService(ctx context.Context, app, env, id string) error {
	return c.Call(ctx, http.MethodDelete, envPath(app, env)+"/services/"+esc(id), url.Values{"confirm": {id}}, nil, nil)
}

func (c *Client) ServiceConnection(ctx context.Context, app, env, id string) (Connection, error) {
	var v Connection
	err := c.Call(ctx, http.MethodGet, envPath(app, env)+"/services/"+esc(id)+"/connection", nil, nil, &v)
	return v, err
}

// ---- cuentas, vínculos, miembros ----

func (c *Client) CreateAccount(ctx context.Context, app string, r CreateAccountReq) (AccountWithKey, error) {
	var v AccountWithKey
	err := c.Call(ctx, http.MethodPost, appPath(app)+"/accounts", nil, r, &v)
	return v, err
}

func (c *Client) RotateKey(ctx context.Context, app, account string, expiresInDays int64) (ServiceAccountKey, error) {
	return c.IssueKey(ctx, app, account, expiresInDays, false)
}

// IssueKey crea una llave nueva. keepPrevious=false ROTA (las anteriores vencen en la gracia del servidor); true AÑADE sin tocar la vida de las anteriores.
func (c *Client) IssueKey(ctx context.Context, app, account string, expiresInDays int64, keepPrevious bool) (ServiceAccountKey, error) {
	var body any
	if expiresInDays > 0 || keepPrevious {
		m := map[string]any{}
		if expiresInDays > 0 {
			m["expires_in_days"] = expiresInDays
		}
		if keepPrevious {
			m["keep_previous"] = true
		}
		body = m
	}
	var v struct {
		KeyFile ServiceAccountKey `json:"key_file"`
	}
	err := c.Call(ctx, http.MethodPost, appPath(app)+"/accounts/"+esc(account)+"/keys", nil, body, &v)
	return v.KeyFile, err
}

// RevokeKey revoca UNA llave de la cuenta (la cuenta y sus otras llaves siguen).
func (c *Client) RevokeKey(ctx context.Context, app, account, keyID string) error {
	return c.Call(ctx, http.MethodDelete, appPath(app)+"/accounts/"+esc(account)+"/keys/"+esc(keyID), nil, nil, nil)
}

func (c *Client) RevokeAccount(ctx context.Context, app, account string) error {
	return c.Call(ctx, http.MethodDelete, appPath(app)+"/accounts/"+esc(account), nil, nil, nil)
}

func (c *Client) CreateBinding(ctx context.Context, app string, r CreateBindingReq) (Binding, error) {
	var b Binding
	err := c.Call(ctx, http.MethodPost, appPath(app)+"/bindings", nil, r, &b)
	return b, err
}

// UpdateBinding edita un vínculo en sitio (proyecto, rama protegida, ramas, condiciones, entornos y rol); el tipo y el emisor no cambian.
func (c *Client) UpdateBinding(ctx context.Context, app, id string, r CreateBindingReq) (Binding, error) {
	var b Binding
	err := c.Call(ctx, http.MethodPut, appPath(app)+"/bindings/"+esc(id), nil, r, &b)
	return b, err
}

func (c *Client) RemoveBinding(ctx context.Context, app, id string) error {
	return c.Call(ctx, http.MethodDelete, appPath(app)+"/bindings/"+esc(id), nil, nil, nil)
}

func (c *Client) AddMember(ctx context.Context, app, email, role string) (Member, error) {
	var m Member
	err := c.Call(ctx, http.MethodPost, appPath(app)+"/members", nil, map[string]string{"email": email, "role": role}, &m)
	return m, err
}

func (c *Client) SetMemberRole(ctx context.Context, app, email, role string) (Member, error) {
	var m Member
	err := c.Call(ctx, http.MethodPut, appPath(app)+"/members/"+esc(email), nil, map[string]string{"role": role}, &m)
	return m, err
}

func (c *Client) RemoveMember(ctx context.Context, app, email string) error {
	return c.Call(ctx, http.MethodDelete, appPath(app)+"/members/"+esc(email), nil, nil, nil)
}

// ---- colas ----

func (c *Client) ListQueues(ctx context.Context) ([]Queue, error) {
	var l QueueList
	err := c.Call(ctx, http.MethodGet, "/queues", nil, nil, &l)
	return l.Items, err
}

func (c *Client) CreateQueue(ctx context.Context, r CreateQueueReq) (Queue, error) {
	var q Queue
	err := c.Call(ctx, http.MethodPost, "/queues", nil, r, &q)
	return q, err
}

func (c *Client) UpdateQueue(ctx context.Context, kind, name string, r UpdateQueueReq) (Queue, error) {
	var q Queue
	err := c.Call(ctx, http.MethodPut, "/queues/"+esc(name), url.Values{"kind": {kind}}, r, &q)
	return q, err
}

func (c *Client) RemoveQueue(ctx context.Context, kind, name string) error {
	return c.Call(ctx, http.MethodDelete, "/queues/"+esc(name), url.Values{"kind": {kind}, "confirm": {name}}, nil, nil)
}

// ---- runners ----

func (c *Client) ListRunnerPools(ctx context.Context) ([]RunnerPool, error) {
	var r Runners
	err := c.Call(ctx, http.MethodGet, "/runners", nil, nil, &r)
	return r.Pools, err
}

func (c *Client) CreateRunnerPool(ctx context.Context, r CreateRunnerPoolReq) (RunnerPool, error) {
	var p RunnerPool
	err := c.Call(ctx, http.MethodPost, "/runners/pools", nil, r, &p)
	return p, err
}

func (c *Client) UpdateRunnerPool(ctx context.Context, name string, r UpdateRunnerPoolReq) (RunnerPool, error) {
	var p RunnerPool
	err := c.Call(ctx, http.MethodPut, "/runners/pools/"+esc(name), nil, r, &p)
	return p, err
}

func (c *Client) RemoveRunnerPool(ctx context.Context, name string) error {
	return c.Call(ctx, http.MethodDelete, "/runners/pools/"+esc(name), url.Values{"confirm": {name}}, nil, nil)
}

// ---- replicación ----

func (c *Client) ProposeLink(ctx context.Context, r ProposeLinkReq) (Proposal, error) {
	var p Proposal
	err := c.Call(ctx, http.MethodPost, "/replication/links", nil, r, &p)
	return p, err
}

func (c *Client) GetLink(ctx context.Context, name string) (ReplLink, error) {
	var l ReplLink
	err := c.Call(ctx, http.MethodGet, "/replication/links/"+esc(name), nil, nil, &l)
	return l, err
}

// ---- administradores y personas ----

func (c *Client) ListAdmins(ctx context.Context) ([]Admin, error) {
	var l struct {
		Items []Admin `json:"items"`
	}
	err := c.Call(ctx, http.MethodGet, "/admins", nil, nil, &l)
	return l.Items, err
}

func (c *Client) AddAdmin(ctx context.Context, email string) (Admin, error) {
	var a Admin
	err := c.Call(ctx, http.MethodPost, "/admins", nil, map[string]string{"email": email}, &a)
	return a, err
}

func (c *Client) RemoveAdmin(ctx context.Context, email string) error {
	return c.Call(ctx, http.MethodDelete, "/admins/"+esc(email), nil, nil, nil)
}

func (c *Client) GetPerson(ctx context.Context, email string) (Person, error) {
	var p Person
	err := c.Call(ctx, http.MethodGet, "/people/"+esc(email), nil, nil, &p)
	return p, err
}

// ---- cuentas de plataforma (solo persona administradora) ----

func (c *Client) CreatePlatformAccount(ctx context.Context, r CreatePlatformAccountReq) (PlatformAccountWithKey, error) {
	var v PlatformAccountWithKey
	err := c.Call(ctx, http.MethodPost, "/platform-accounts", nil, r, &v)
	return v, err
}

func (c *Client) ListPlatformAccounts(ctx context.Context) ([]PlatformAccount, error) {
	var l struct {
		Items []PlatformAccount `json:"items"`
	}
	err := c.Call(ctx, http.MethodGet, "/platform-accounts", nil, nil, &l)
	return l.Items, err
}

func (c *Client) RotatePlatformAccount(ctx context.Context, name string, days int64) (PlatformAccountWithKey, error) {
	var v PlatformAccountWithKey
	err := c.Call(ctx, http.MethodPost, "/platform-accounts/"+esc(name)+"/keys", nil, map[string]int64{"expires_in_days": days}, &v)
	return v, err
}

func (c *Client) RevokePlatformAccount(ctx context.Context, name string) error {
	return c.Call(ctx, http.MethodDelete, "/platform-accounts/"+esc(name), nil, nil, nil)
}
