// Package fakeapi es un servidor simulado de plane + la API /v1 de Bifröst, en memoria. Reproduce el contrato documentado en
// docs/inventario-api.md (rutas, validaciones, códigos de error, borrado en dos fases, secretos que solo viajan al crear) para probar el
// provider sin red ni cluster. No es una implementación del servidor real: si el servidor cambia, el inventario y este fake se actualizan juntos.
package fakeapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tokens y credenciales de prueba (valores de ejemplo, no reales).
const (
	AdminIDToken    = "id-token-admin-ejemplo" // grant=user -> persona administradora
	CIIDToken       = "id-token-ci-ejemplo"    // grant=federated -> máquina ligada a CIApp
	DirectAdminTok  = "token-directo-ejemplo"  // Bearer ya emitido (administrador)
	CIApp           = "tienda"                 // App a la que está vinculado el ID token del CI
	BootstrapAdmin  = "root@example.com"       // administrador de arranque
	defaultOwner    = "admin@example.com"      // owner por omisión cuando no se indica
	ReservedAPIPath = "/__fake__"
)

type quota struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	Pods   int    `json:"pods"`
	PVCs   int    `json:"pvcs"`
}

type egress struct {
	CIDR   string `json:"cidr"`
	Port   int    `json:"port"`
	Reason string `json:"reason"`
}

type network struct {
	Domains       []string `json:"domains"`
	Internet      bool     `json:"internet"`
	InternetPorts []int    `json:"internet_ports"`
	Egress        []egress `json:"egress"`
}

type service struct {
	Service    string   `json:"service"`
	SecretName string   `json:"secret_name"`
	Dedicated  bool     `json:"dedicated"`
	Resources  []string `json:"resources"`
	AddedBy    string   `json:"added_by"`
	AddedAt    string   `json:"added_at"`
	opts       map[string]string
}

type accessRule struct {
	From    string
	Service string
	Port    int
}

type workload struct {
	Kind     string
	Name     string
	Replicas int
	HPA      map[string]any
	Vertical map[string]any
}

type env struct {
	Name       string
	Status     string
	Error      string
	Quota      quota
	ArchivedAt time.Time
	Services   []*service
	Network    network
	Access     []accessRule
	Workloads  []*workload
	Hosts      map[string]map[string]any
	Schedule   *scheduleState
}

type key struct {
	ID      string
	Public  ed25519.PublicKey
	Created time.Time
	Expires *time.Time
	Revoked bool
}

type account struct {
	Name    string
	Role    string
	Envs    []string
	Revoked bool
	Keys    []*key
}

type binding struct {
	ID           string
	Kind, Issuer string
	ProjectPath  string
	RefProtected bool
	Claims       map[string]string
	Role         string
	Envs         []string
	Branches     []string
}

type member struct{ Email, Role string }

type app struct {
	Name, Display, Desc string
	Group               string
	Quota               quota
	Envs                []*env
	Accounts            []*account
	Bindings            []*binding
	Members             []member
	Pool                *string
	Repl                []string
	CreatedBy           string
	ManagedBy           string
}

type queue struct {
	Kind, Name, Display string
	Hidden              bool
	JS                  map[string]any
}

type pool struct {
	Name, Scope, App string
	Labels, Origins  []string
	Min, Max         int
	Paused           bool
}

type link struct {
	Name, Source, Dest, Mode, App string
	Approved                      bool
}

type identity struct {
	PlatformName string
	Email        string
	Admin        bool
	Machine      bool
	App          string
}

// Server es el servidor simulado.
type Server struct {
	mu sync.Mutex
	// Retention: si es true, purgar algo recién archivado responde 409 (retención en curso).
	Retention bool
	// FailProvision: nombres "app/env" cuyo aprovisionamiento falla (queda en error) hasta que se quite de aquí.
	FailProvision map[string]bool
	// Requests registra "MÉTODO ruta" de cada petición /v1 (para verificar que no hay llamadas de más).
	Requests []string
	// Bodies registra los cuerpos recibidos (para comprobar que no se filtran secretos en logs del provider, etc.).
	Authorizations []string

	URL      string
	plat     map[string]*platAccount
	apps     map[string]*app
	queues   []*queue
	pools    []*pool
	links    []*link
	admins   []string
	tokens   map[string]identity
	jtis     map[string]bool
	nextID   int
	catalog  map[string]catalogEntry
	httpSrv  *httptest.Server
	now      func() time.Time
	rejected int
}

type catalogEntry struct {
	Name, Isolation string
	Dedicated       bool
	Secret          string
	Options         []map[string]any
}

// New arma el servidor sin escuchar.
func New() *Server {
	s := &Server{apps: map[string]*app{}, tokens: map[string]identity{}, jtis: map[string]bool{}, FailProvision: map[string]bool{},
		admins: []string{BootstrapAdmin}, now: time.Now}
	s.catalog = map[string]catalogEntry{
		"postgres": {Name: "PostgreSQL", Isolation: "shared", Dedicated: true, Secret: "postgres-conn",
			Options: []map[string]any{{"key": "database", "type": "string", "default": "", "description": "nombre de la base", "enum": []string{}, "admin_only": true},
				{"key": "version", "type": "enum", "default": "16", "enum": []string{"15", "16"}, "description": "versión"}}},
		"redis": {Name: "Redis", Isolation: "shared", Dedicated: false, Secret: "redis-conn", Options: []map[string]any{}},
	}
	return s
}

// Start levanta un httptest.Server y devuelve su URL (http://127.0.0.1:puerto).
func (s *Server) Start() string {
	s.httpSrv = httptest.NewServer(s)
	s.URL = s.httpSrv.URL
	return s.URL
}

// Close apaga el servidor.
func (s *Server) Close() {
	if s.httpSrv != nil {
		s.httpSrv.Close()
	}
}

// Lock/Unlock permiten a las pruebas tocar el estado ("drift") de forma segura.
func (s *Server) Lock()   { s.mu.Lock() }
func (s *Server) Unlock() { s.mu.Unlock() }

// ---- ayudas de prueba (llamar sin Lock; toman el cerrojo ellas mismas) ----

// Drop borra una App del estado sin pasar por la API (simula borrado externo).
func (s *Server) Drop(appName string) { s.mu.Lock(); delete(s.apps, appName); s.mu.Unlock() }

// DropEnv quita un entorno sin pasar por la API.
func (s *Server) DropEnv(appName, envName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[appName]; a != nil {
		for i, e := range a.Envs {
			if e.Name == envName {
				a.Envs = append(a.Envs[:i], a.Envs[i+1:]...)
				return
			}
		}
	}
}

// Mutate ejecuta fn con el cerrojo tomado (para cambiar el estado "por fuera").
func (s *Server) Mutate(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); fn() }

// SetDescription cambia la descripción de una App por fuera (drift).
func (s *Server) SetDescription(appName, d string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[appName]; a != nil {
		a.Desc = d
	}
}

// SetQuota cambia la cuota por omisión por fuera (drift).
func (s *Server) SetQuotaOutside(appName string, pods int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[appName]; a != nil {
		a.Quota.Pods = pods
	}
}

// SetNetworkOutside cambia los dominios de un entorno por fuera (drift).
func (s *Server) SetNetworkOutside(appName, envName string, domains []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findEnv(appName, envName); e != nil {
		e.Network.Domains = domains
	}
}

// AddWorkload simula una carga desplegada (Deployment/StatefulSet) en un entorno.
func (s *Server) AddWorkload(appName, envName, kind, name string, replicas int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findEnv(appName, envName); e != nil {
		e.Workloads = append(e.Workloads, &workload{Kind: kind, Name: name, Replicas: replicas})
	}
}

// ApproveLink simula la aprobación de un enlace en Ratatoskr.
func (s *Server) ApproveLink(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.Name == name {
			l.Approved = true
		}
	}
}

// RemoveLink borra un enlace por fuera.
func (s *Server) RemoveLink(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.Name == name {
			s.links = append(s.links[:i], s.links[i+1:]...)
			return
		}
	}
}

// AppExists dice si la App existe (aunque esté archivada) y su estado derivado.
func (s *Server) AppExists(name string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.apps[name]
	if a == nil {
		return false, ""
	}
	return true, s.appStatus(a)
}

// AccountRevoked dice si la cuenta existe y si está revocada.
func (s *Server) AccountRevoked(appName, acc string) (exists, revoked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[appName]; a != nil {
		for _, c := range a.Accounts {
			if c.Name == acc {
				return true, c.Revoked
			}
		}
	}
	return false, false
}

// RevokeKeyOutside revoca una clave por fuera.
func (s *Server) RevokeKeyOutside(appName, acc, keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[appName]; a != nil {
		for _, c := range a.Accounts {
			if c.Name == acc {
				for _, k := range c.Keys {
					if k.ID == keyID {
						k.Revoked = true
					}
				}
			}
		}
	}
}

// RequestCount cuenta las peticiones registradas que empiezan con el prefijo ("POST /v1/apps").
func (s *Server) RequestCount(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// ---- utilidades ----

var nameRe = regexp.MustCompile(`^[a-z](?:[a-z0-9]|-[a-z0-9]){1,28}$`)

var reserved = map[string]bool{"system": true, "default": true, "infra": true, "kube": true, "bifrost": true, "plane": true}

func slug(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type apiErr struct {
	code int
	msg  string
}

func e400(m string) *apiErr { return &apiErr{400, m} }
func e403(m string) *apiErr { return &apiErr{403, m} }
func e404(m string) *apiErr { return &apiErr{404, m} }
func e409(m string) *apiErr { return &apiErr{409, m} }

func checkSlug(raw, of string, max int) (string, *apiErr) {
	n := slug(raw)
	switch {
	case n == "":
		return "", e400("El nombre " + of + " no tiene letras ni números.")
	case n[0] < 'a' || n[0] > 'z':
		return "", e400("El nombre " + of + " debe empezar con una letra (quedó «" + n + "»).")
	case len(n) < 2:
		return "", e400("El nombre " + of + " debe tener al menos 2 caracteres.")
	case len(n) > max:
		return "", e400("El nombre " + of + " es demasiado largo: «" + n + "» tiene " + strconv.Itoa(len(n)) + " caracteres y el máximo es " + strconv.Itoa(max) + ".")
	}
	return n, nil
}

var cpuRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?m?$`)
var memRe = regexp.MustCompile(`^[0-9]+(Mi|Gi|Ki|M|G)$`)

func validQuota(q quota) *apiErr {
	if q.CPU == "" || !cpuRe.MatchString(q.CPU) {
		return e400("La cuota de CPU no es válida (ej. 2 o 500m).")
	}
	if !memRe.MatchString(q.Memory) {
		return e400("La cuota de memoria no es válida (ej. 4Gi).")
	}
	if q.Pods < 1 || q.Pods > 500 || q.PVCs < 0 || q.PVCs > 100 {
		return e400("La cuota de pods (1 a 500) o de volúmenes (0 a 100) no es válida.")
	}
	return nil
}

func (s *Server) id() string {
	s.nextID++
	return fmt.Sprintf("b%06d", s.nextID)
}

func (s *Server) findEnv(appName, envName string) *env {
	if a := s.apps[appName]; a != nil {
		for _, e := range a.Envs {
			if e.Name == envName {
				return e
			}
		}
	}
	return nil
}

func (s *Server) appStatus(a *app) string {
	if len(a.Envs) == 0 {
		return "active"
	}
	archived, errs := 0, 0
	for _, e := range a.Envs {
		switch e.Status {
		case "archived":
			archived++
		case "error":
			errs++
		}
	}
	switch {
	case archived == len(a.Envs):
		return "archived"
	case errs > 0:
		return "error"
	}
	return "active"
}

func (s *Server) provision(a *app, e *env) *apiErr {
	if s.FailProvision[a.Name+"/"+e.Name] {
		e.Status, e.Error = "error", "No se pudo aprovisionar el namespace."
		return &apiErr{502, "No se pudo aprovisionar el entorno «" + e.Name + "»; queda en error y se puede reintentar."}
	}
	e.Status, e.Error, e.ArchivedAt = "active", "", time.Time{}
	return nil
}

func nz[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

func (s *Server) envJSON(a *app, e *env) map[string]any {
	n := e.Network
	ports := []int{}
	if n.Internet {
		ports = n.InternetPorts
		if len(ports) == 0 {
			ports = []int{443, 80}
		}
	}
	var errp *string
	if e.Error != "" {
		x := e.Error
		errp = &x
	}
	var arch *string
	if !e.ArchivedAt.IsZero() {
		x := e.ArchivedAt.UTC().Format(time.RFC3339)
		arch = &x
	}
	svcs := []map[string]any{}
	for _, v := range e.Services {
		svcs = append(svcs, svcJSON(v))
	}
	return map[string]any{"name": e.Name, "namespace": "app-" + a.Name + "-" + e.Name, "status": e.Status, "error": errp, "quota": e.Quota,
		"hosts": append([]string{e.Name + "." + a.Name + ".example.com"}, n.Domains...), "created_by": a.CreatedBy, "created_at": "2026-01-01T00:00:00Z", "archived_at": arch,
		"services": svcs, "network": map[string]any{"domains": nz(n.Domains), "internet": n.Internet, "internet_ports": ports, "egress": nz(n.Egress)}}
}

func svcJSON(v *service) map[string]any {
	return map[string]any{"service": v.Service, "secret_name": v.SecretName, "dedicated": v.Dedicated, "resources": nz(v.Resources), "added_by": v.AddedBy, "added_at": v.AddedAt}
}

func (s *Server) appJSON(a *app) map[string]any {
	envs := []map[string]any{}
	for _, e := range a.Envs {
		envs = append(envs, s.envJSON(a, e))
	}
	accs := []map[string]any{}
	for _, c := range a.Accounts {
		accs = append(accs, accountJSON(a.Name, c))
	}
	bs := []map[string]any{}
	for _, b := range a.Bindings {
		bs = append(bs, bindingJSON(b))
	}
	ms := []map[string]any{}
	for _, m := range a.Members {
		ms = append(ms, map[string]any{"email": m.Email, "role": m.Role, "added_by": "x", "added_at": "2026-01-01T00:00:00Z"})
	}
	st := s.appStatus(a)
	return map[string]any{"name": a.Name, "display_name": a.Display, "description": a.Desc, "group": a.Group, "status": st, "error": nil, "quota": a.Quota, "created_by": a.CreatedBy,
		"created_at": "2026-01-01T00:00:00Z", "archived_at": nil, "envs": envs, "accounts": accs, "bindings": bs, "members": ms,
		"links": map[string]any{"pool": a.Pool, "replication": nz(a.Repl)}, "access": "admin", "managed_by": a.ManagedBy}
}

func accountJSON(app string, c *account) map[string]any {
	ks := []map[string]any{}
	for _, k := range c.Keys {
		var exp *string
		if k.Expires != nil {
			x := k.Expires.UTC().Format(time.RFC3339)
			exp = &x
		}
		ks = append(ks, map[string]any{"id": k.ID, "created": k.Created.UTC().Format(time.RFC3339), "expires": exp, "revoked": k.Revoked})
	}
	return map[string]any{"name": c.Name, "id": c.Name + "@app-" + app, "role": c.Role, "created_by": "x", "created_at": "2026-01-01T00:00:00Z",
		"revoked": c.Revoked, "last_used": nil, "keys": ks, "envs": nz(c.Envs)}
}

func bindingJSON(b *binding) map[string]any {
	cl := b.Claims
	if cl == nil {
		cl = map[string]string{}
	}
	return map[string]any{"id": b.ID, "kind": b.Kind, "issuer": b.Issuer, "project_path": b.ProjectPath, "ref_protected": b.RefProtected, "claims": cl,
		"role": b.Role, "envs": nz(b.Envs), "branches": nz(b.Branches), "created_by": "x", "created_at": "2026-01-01T00:00:00Z", "last_exchange": nil}
}

// ---- HTTP ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *apiErr) {
	writeJSON(w, e.code, map[string]string{"error": e.msg})
}

// ServeHTTP enruta. Todo bajo el mismo cerrojo: las pruebas son secuenciales y esto mantiene el fake simple.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/.well-known/bifrost" && r.Method == http.MethodGet:
		base := "http://" + r.Host
		writeJSON(w, 200, map[string]any{"issuer": base, "api": base + "/v1", "token_uri": base + "/auth/token", "jwks_uri": base + "/.well-known/jwks.json",
			"sessions_uri": base + "/v1/sessions", "cluster": "gravity", "version": "fake"})
		return
	case path == "/healthz":
		_, _ = w.Write([]byte("ok"))
		return
	case path == "/auth/token" && r.Method == http.MethodPost:
		s.token(w, r)
		return
	case strings.HasPrefix(path, "/v1/"):
		s.v1(w, r)
		return
	}
	writeErr(w, e404("No existe esa ruta."))
}

func (s *Server) newToken(id identity) (string, int) {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	t := "bt-" + hex.EncodeToString(b)
	s.tokens[t] = id
	return t, 3600
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Grant, Assertion string
		IDToken          string `json:"id_token"`
		App              string
		Platform         string
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeErr(w, e400("Cuerpo no válido."))
		return
	}
	deny := &apiErr{401, "Credencial no válida."}
	var id identity
	switch req.Grant {
	case "user":
		if req.IDToken != AdminIDToken {
			writeErr(w, deny)
			return
		}
		id = identity{Email: BootstrapAdmin, Admin: true}
	case "federated":
		if req.Platform != "" {
			a := s.platAcc(req.Platform)
			if a == nil || !a.active(s.now()) || !a.CI || req.IDToken != CIIDToken {
				writeErr(w, deny)
				return
			}
			id = identity{Email: "sa:" + a.Name + "@platform", Machine: true, PlatformName: a.Name}
			break
		}
		if req.IDToken != CIIDToken || (req.App != "" && req.App != CIApp) {
			writeErr(w, deny)
			return
		}
		id = identity{Email: "ci:grupo/tienda", Machine: true, App: CIApp}
	case "service_account":
		var ok bool
		id, ok = s.verifyAssertion(req.Assertion, "http://"+r.Host+"/auth/token")
		if !ok {
			writeErr(w, deny)
			return
		}
	default:
		writeErr(w, e400("Concesión no admitida."))
		return
	}
	t, exp := s.newToken(id)
	writeJSON(w, 200, map[string]any{"access_token": t, "token_type": "Bearer", "expires_in": exp})
}

func (s *Server) verifyAssertion(a, aud string) (identity, bool) {
	parts := strings.Split(a, ".")
	if len(parts) != 3 {
		return identity{}, false
	}
	dec := func(x string) []byte { b, _ := base64.RawURLEncoding.DecodeString(x); return b }
	var h struct{ Alg, Kid string }
	var c struct {
		Iss, Sub, Aud, Jti string
		Exp                int64
	}
	if json.Unmarshal(dec(parts[0]), &h) != nil || json.Unmarshal(dec(parts[1]), &c) != nil {
		return identity{}, false
	}
	if h.Alg != "EdDSA" || c.Iss == "" || c.Iss != c.Sub || c.Aud != aud || c.Jti == "" || time.Unix(c.Exp, 0).Before(s.now()) ||
		time.Unix(c.Exp, 0).After(s.now().Add(5*time.Minute+30*time.Second)) {
		return identity{}, false
	}
	if pn, isPlat := strings.CutSuffix(c.Iss, "@platform"); isPlat {
		return s.verifyPlatformAssertion(pn, parts, h, c.Jti, c.Exp)
	}
	name, appName, ok := strings.Cut(c.Iss, "@app-")
	if !ok {
		return identity{}, false
	}
	ap := s.apps[appName]
	if ap == nil {
		return identity{}, false
	}
	for _, ac := range ap.Accounts {
		if ac.Name != name || ac.Revoked {
			continue
		}
		for _, k := range ac.Keys {
			if k.ID != h.Kid || k.Revoked || (k.Expires != nil && !k.Expires.After(s.now())) {
				continue
			}
			if !ed25519.Verify(k.Public, []byte(parts[0]+"."+parts[1]), dec(parts[2])) {
				return identity{}, false
			}
			if s.jtis[c.Iss+"|"+c.Jti] {
				return identity{}, false
			}
			s.jtis[c.Iss+"|"+c.Jti] = true
			return identity{Email: "sa:" + c.Iss, Machine: true, App: appName}, true
		}
	}
	return identity{}, false
}

func newKeyPair() (ed25519.PublicKey, string) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	return pub, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func (s *Server) keyFile(appName string, ac *account, k *key, priv string) map[string]any {
	return map[string]any{"type": "bifrost_service_account", "api": s.URL, "account": ac.Name + "@app-" + appName, "private_key_id": k.ID, "private_key": priv}
}

// v1 aplica la autenticación y la tabla de rutas.
func (s *Server) v1(w http.ResponseWriter, r *http.Request) {
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	auth := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	var id identity
	if tok == DirectAdminTok {
		id, ok = identity{Email: BootstrapAdmin, Admin: true}, true
	} else if ok {
		id, ok = s.tokens[tok]
	}
	if !ok {
		writeErr(w, &apiErr{401, "Token no válido."})
		return
	}
	s.Authorizations = append(s.Authorizations, tok)
	rest := strings.TrimPrefix(r.URL.Path, "/v1")
	if id.Machine && id.PlatformName == "" && !machineAllowed(id, r.Method, rest) {
		writeErr(w, e403("Esta cuenta no puede usar esa ruta."))
		return
	}
	seg := strings.Split(strings.Trim(rest, "/"), "/")
	for i := range seg {
		if u, err := urlUnescape(seg[i]); err == nil {
			seg[i] = u
		}
	}
	var body []byte
	if r.Body != nil {
		body, _ = readAll(r)
	}
	if id.PlatformName != "" {
		if e := s.platAuthorize(&id, r, seg, body); e != nil {
			writeErr(w, e)
			return
		}
		id.Admin = true // superadas las comprobaciones de scope y de App, la operación se ejecuta como la del servidor
	}
	code, out, e := s.route(id, r, seg, body)
	if e != nil {
		writeErr(w, e)
		return
	}
	writeJSON(w, code, out)
}

func machineAllowed(id identity, method, rest string) bool {
	switch {
	case rest == "/me":
		return method == http.MethodGet
	case rest == "/services" || strings.HasPrefix(rest, "/services/"):
		return method == http.MethodGet
	}
	r, ok := strings.CutPrefix(rest, "/apps/")
	if !ok {
		return false
	}
	appName, sub, _ := strings.Cut(r, "/")
	if appName != id.App || strings.HasPrefix(sub, "accounts") || strings.HasPrefix(sub, "bindings") || strings.HasPrefix(sub, "members") {
		return false
	}
	return method == http.MethodGet
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SetEnvStatus fuerza el estado de un entorno (llamar dentro de Mutate o con el cerrojo ya tomado).
func (s *Server) SetEnvStatus(appName, envName, status string) {
	if e := s.findEnv(appName, envName); e != nil {
		e.Status = status
	}
}

// CountState devuelve una descripción de lo que queda vivo (colas, pools, enlaces, administradores extra, miembros extra); "" si nada.
func (s *Server) CountState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ""
	if len(s.queues) > 0 {
		out += fmt.Sprintf(" colas=%d", len(s.queues))
	}
	if len(s.pools) > 0 {
		out += fmt.Sprintf(" pools=%d", len(s.pools))
	}
	if len(s.admins) > 1 {
		out += fmt.Sprintf(" admins=%d", len(s.admins))
	}
	if a := s.apps["tienda"]; a != nil {
		if len(a.Bindings) > 0 {
			out += fmt.Sprintf(" vínculos=%d", len(a.Bindings))
		}
		if len(a.Members) > 1 {
			out += fmt.Sprintf(" miembros=%d", len(a.Members))
		}
		for _, e := range a.Envs {
			if len(e.Services) > 0 {
				out += " servicios"
			}
		}
	}
	return out
}

// RevokeAccessOutside quita una regla de acceso por fuera (drift).
func (s *Server) RevokeAccessOutside(appName, envName, from, svc string, port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findEnv(appName, envName); e != nil {
		for i, r := range e.Access {
			if r.From == from && r.Service == svc && r.Port == port {
				e.Access = append(e.Access[:i], e.Access[i+1:]...)
				return
			}
		}
	}
}

// Listen levanta el servidor en addr (p. ej. "127.0.0.1:0") y devuelve la URL; es para `cmd/fakeplane` (smoke con el terraform real).
func (s *Server) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.URL = "http://" + ln.Addr().String()
	go func() { _ = http.Serve(ln, s) }()
	return s.URL, nil
}
