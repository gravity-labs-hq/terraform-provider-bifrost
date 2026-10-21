package fakeapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Cuentas de servicio de PLATAFORMA (ver bifrost/docs/iac.md). El fake reproduce el contrato del servidor: scopes por ruta con default-deny,
// Apps permitidas (creadas por la cuenta, asignadas o dentro del prefijo), owners humanos obligatorios al crear y sin purgar ni restaurar.

// PlatformScopes son los scopes válidos (internal/scope del servidor).
var PlatformScopes = []string{"apps:read", "apps:create", "apps:manage", "quota:manage", "network:manage", "members:manage", "identities:manage", "queues:read",
	"queues:manage", "runners:read", "runners:manage", "replication:read", "replication:propose", "schedule:manage", "ports:manage"}

var implied = map[string][]string{"apps:create": {"apps:read"}, "apps:manage": {"apps:read"}, "quota:manage": {"apps:read"}, "network:manage": {"apps:read"},
	"members:manage": {"apps:read"}, "identities:manage": {"apps:read"}, "queues:manage": {"queues:read"}, "runners:manage": {"runners:read"},
	"replication:propose": {"replication:read"}, "schedule:manage": {"apps:read"}, "ports:manage": {"apps:read"}}

type platAccount struct {
	Name    string
	Scopes  map[string]bool
	Apps    []string
	Prefix  string
	Keys    []*key
	Revoked bool
	Expires time.Time
	Domains []string
	CI      bool // tiene vínculo de CI (el ID token CIIDToken puede asumirla)
	// Bindings: vínculos de CI con sus condiciones extra (claims), como los devuelve el servidor.
	Bindings []platBinding
}

type platBinding struct {
	ProjectPath, Environment string
	Claims                   map[string]string
}

func (a *platAccount) active(now time.Time) bool { return !a.Revoked && now.Before(a.Expires) }

func (a *platAccount) permitsName(n string) bool {
	for _, x := range a.Apps {
		if x == n {
			return true
		}
	}
	return a.Prefix != "" && strings.HasPrefix(n, a.Prefix)
}

func (a *platAccount) permits(ap *app) bool { return ap.ManagedBy == a.Name || a.permitsName(ap.Name) }

func (s *Server) platAcc(name string) *platAccount {
	if s.plat == nil {
		return nil
	}
	return s.plat[name]
}

// scopeFor devuelve el scope que exige la ruta a una cuenta de plataforma ("" = cerrada).
func scopeFor(method string, seg []string) string {
	read := method == http.MethodGet
	pick := func(r, w string) string {
		if read {
			return r
		}
		return w
	}
	switch seg[0] {
	case "me":
		return "me"
	case "services":
		if read {
			return "apps:read"
		}
	case "queues":
		return pick("queues:read", "queues:manage")
	case "runners":
		return pick("runners:read", "runners:manage")
	case "replication":
		if read && len(seg) == 3 {
			return "replication:read"
		}
		if method == http.MethodPost && len(seg) == 2 {
			return "replication:propose"
		}
	case "apps":
		switch {
		case len(seg) == 1:
			return pick("apps:read", "apps:create")
		case len(seg) == 2:
			if method == http.MethodGet {
				return "apps:read"
			}
			if method == http.MethodPatch || method == http.MethodDelete {
				return "apps:manage"
			}
		default:
			switch seg[2] {
			case "quota":
				return "quota:manage"
			case "links", "reconcile":
				return "apps:manage"
			case "members":
				return "members:manage"
			case "accounts", "bindings":
				return "identities:manage"
			case "envs":
				return envScope(method, seg, pick)
			}
		}
	}
	return ""
}

func envScope(method string, seg []string, pick func(r, w string) string) string {
	switch len(seg) {
	case 3:
		return pick("apps:read", "apps:manage")
	case 4:
		if method == http.MethodGet {
			return "apps:read"
		}
		if method == http.MethodDelete {
			return "apps:manage"
		}
	default:
		switch seg[4] {
		case "reconcile":
			return "apps:manage"
		case "network":
			if method == http.MethodPut {
				return "network:manage"
			}
		case "schedule":
			return "schedule:manage" // lectura y escritura: nadie más la abre (default-deny)
		case "egress", "ports":
			return "ports:manage" // lectura y escritura: ningún otro scope la abre (default-deny)
		case "access":
			return pick("apps:read", "network:manage")
		case "hosts":
			return pick("apps:read", "network:manage")
		case "scaling":
			if method == http.MethodGet && len(seg) == 5 {
				return "apps:read"
			}
			if method == http.MethodPut && len(seg) == 7 {
				return "apps:manage"
			}
		case "services":
			switch {
			case len(seg) == 5:
				return pick("apps:read", "apps:manage")
			case len(seg) == 6 && method == http.MethodDelete:
				return "apps:manage"
			case len(seg) == 7 && seg[6] == "connection" && method == http.MethodGet:
				return "apps:read"
			}
		}
	}
	return ""
}

func (s *Server) platAuthorize(id *identity, r *http.Request, seg []string, body []byte) *apiErr {
	acc := s.platAcc(id.PlatformName)
	if acc == nil || !acc.active(s.now()) {
		return &apiErr{401, "Token no válido."}
	}
	sc := scopeFor(r.Method, seg)
	if sc == "me" {
		return nil
	}
	if sc == "" || !acc.Scopes[sc] {
		return e403("Esta cuenta de plataforma no puede usar esa ruta.")
	}
	q := r.URL.Query()
	if r.Method == http.MethodDelete && q.Get("purge") == "true" {
		return e403("Solo un administrador puede hacer esto.")
	}
	switch seg[0] {
	case "apps":
		if len(seg) == 1 && r.Method == http.MethodPost {
			var req struct {
				Name   string
				Owners []string
				Quota  *quota
			}
			if json.Unmarshal(body, &req) == nil {
				n := slug(req.Name)
				if (len(acc.Apps) > 0 || acc.Prefix != "") && !acc.permitsName(n) {
					return e403("Esta cuenta de plataforma solo puede crear Apps dentro de su lista o prefijo.")
				}
				if len(req.Owners) == 0 {
					return e400("Indica al menos un owner humano (owners).")
				}
				if req.Quota != nil && !acc.Scopes["quota:manage"] {
					return e403("Fijar cuotas al crear exige el scope quota:manage.")
				}
				if acc.Prefix == "" {
					return e403("apps:create exige que la cuenta tenga app_prefix; esta cuenta no lo tiene.")
				}
				for _, o := range req.Owners {
					if !acc.domainOK(o) {
						return e403("El owner «" + o + "» está fuera de los dominios permitidos de esta cuenta de plataforma.")
					}
				}
			}
		}
		if len(seg) >= 2 {
			ap := s.apps[seg[1]]
			if ap == nil || !acc.permits(ap) {
				return e404("No existe esa App.")
			}
		}
		if len(seg) >= 3 && seg[2] == "envs" && len(seg) == 3 && r.Method == http.MethodPost {
			var req struct{ Quota *quota }
			if json.Unmarshal(body, &req) == nil && req.Quota != nil && !acc.Scopes["quota:manage"] {
				return e403("Fijar la cuota de un entorno exige el scope quota:manage.")
			}
		}
		if len(seg) == 5 && seg[4] == "access" && r.Method != http.MethodGet {
			from := q.Get("from")
			if r.Method == http.MethodPost {
				var req struct{ From string }
				_ = json.Unmarshal(body, &req)
				from = req.From
			}
			if o := s.apps[from]; o == nil || !acc.permits(o) {
				return e400("No existe la App de origen «" + from + "».")
			}
		}
		if len(seg) == 3 && seg[2] == "members" && r.Method != http.MethodDelete {
			var req struct{ Email string }
			_ = json.Unmarshal(body, &req)
			if !acc.domainOK(req.Email) {
				return e403("El correo está fuera de los dominios permitidos de esta cuenta de plataforma.")
			}
		}
		if len(seg) == 3 && seg[2] == "accounts" && r.Method == http.MethodPost {
			var req struct{ Envs []string }
			_ = json.Unmarshal(body, &req)
			if len(req.Envs) == 0 {
				return e400("Una cuenta de plataforma debe indicar los entornos de la cuenta de servicio (no puede crear una con acceso a «todos, presentes y futuros»).")
			}
		}
		if len(seg) == 3 && seg[2] == "bindings" && r.Method == http.MethodPost {
			var req struct {
				ProjectPath  string `json:"project_path"`
				RefProtected bool   `json:"ref_protected"`
				Claims       map[string]string
				Envs         []string
			}
			_ = json.Unmarshal(body, &req)
			if !req.RefProtected || strings.TrimSpace(req.Claims["environment"]) == "" || strings.Contains(req.ProjectPath, "*") || len(req.Envs) == 0 {
				return e400("Un vínculo creado por una cuenta de plataforma exige ref_protected, la condición environment, un proyecto exacto (sin comodines) y entornos explícitos.")
			}
		}
	case "queues", "runners", "replication":
		name := ""
		if len(seg) >= 2 && seg[0] == "queues" {
			name = seg[1]
		}
		if len(seg) >= 3 && seg[0] == "runners" {
			name = seg[2]
		}
		if len(seg) >= 3 && seg[0] == "replication" {
			name = seg[2]
		}
		var req struct{ Name, App string }
		_ = json.Unmarshal(body, &req)
		if req.Name != "" {
			name = req.Name
		}
		if name != "" && !strings.HasPrefix(name, acc.Prefix) {
			return e403("El nombre queda fuera del prefijo de esta cuenta de plataforma.")
		}
		if req.App != "" {
			if o := s.apps[req.App]; o == nil || !acc.permits(o) {
				return e404("No existe esa App.")
			}
		}
	}
	return nil
}

// ---- administración de las cuentas (solo persona administradora) ----

func bindingsJSON(a *platAccount) []any {
	out := []any{}
	for i, b := range a.Bindings {
		cl := b.Claims
		if cl == nil {
			cl = map[string]string{}
		}
		out = append(out, map[string]any{"id": fmt.Sprintf("e%011d", i), "issuer": "https://gitlab.com", "project_path": b.ProjectPath, "environment": b.Environment, "claims": cl})
	}
	return out
}

func platJSON(a *platAccount) map[string]any {
	sc := make([]string, 0, len(a.Scopes))
	for k := range a.Scopes {
		sc = append(sc, k)
	}
	sort.Strings(sc)
	ks := []map[string]any{}
	for _, k := range a.Keys {
		var exp any
		if k.Expires != nil {
			exp = k.Expires.UTC().Format(time.RFC3339)
		}
		ks = append(ks, map[string]any{"id": k.ID, "created": k.Created.UTC().Format(time.RFC3339), "expires": exp, "revoked": k.Revoked})
	}
	return map[string]any{"name": a.Name, "id": a.Name + "@platform", "scopes": sc, "apps": nz(a.Apps), "app_prefix": a.Prefix, "allowed_domains": nz(a.Domains), "allow_identities": a.Scopes["identities:manage"], "allow_ports": a.Scopes["ports:manage"],
		"expires_at": a.Expires.UTC().Format(time.RFC3339), "created_by": "x", "created_at": "2026-01-01T00:00:00Z", "revoked": a.Revoked, "last_used": nil,
		"keys": ks, "bindings": bindingsJSON(a)}
}

func (s *Server) platformAccounts(m string, seg []string, body []byte) (int, any, *apiErr) {
	if s.plat == nil {
		s.plat = map[string]*platAccount{}
	}
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, k := range sortedKeys(s.plat) {
				items = append(items, platJSON(s.plat[k]))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct {
				Name            string
				Scopes, Apps    []string
				AppPrefix       string   `json:"app_prefix"`
				ExpiresInDays   int      `json:"expires_in_days"`
				AllowedDomains  []string `json:"allowed_domains"`
				AllowIdentities bool     `json:"allow_identities"`
				AllowPorts      bool     `json:"allow_ports"`
				Bindings        []struct {
					ProjectPath string            `json:"project_path"`
					Environment string            `json:"environment"`
					Claims      map[string]string `json:"claims"`
				}
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			if !nameRe.MatchString(req.Name) || strings.HasPrefix(req.Name, "admin") {
				return 0, nil, e400("El nombre de la cuenta debe tener de 2 a 30 caracteres ASCII en minúsculas, números y guiones medios, empezar con letra y no empezar con «admin».")
			}
			if req.ExpiresInDays < 1 || req.ExpiresInDays > 365 {
				return 0, nil, e400("La caducidad es obligatoria: de 1 a 365 días (se renueva rotando la clave).")
			}
			sc := map[string]bool{}
			for _, x := range req.Scopes {
				ok := false
				for _, v := range PlatformScopes {
					ok = ok || v == x
				}
				if !ok {
					return 0, nil, e400("El scope «" + x + "» no existe. No hay scopes sobre administradores, personas ni cuentas de plataforma.")
				}
				sc[x] = true
				for _, i := range implied[x] {
					sc[i] = true
				}
			}
			if len(sc) == 0 {
				return 0, nil, e400("Indica al menos un scope (y no más de 20).")
			}
			if sc["apps:create"] && req.AppPrefix == "" {
				return 0, nil, e400("El scope apps:create exige app_prefix: sin él la cuenta podría crear Apps con cualquier nombre.")
			}
			if sc["identities:manage"] && !req.AllowIdentities {
				return 0, nil, e400("identities:manage es opt-in: añade allow_identities (--allow-identities). Da acceso de despliegue a las Apps permitidas.")
			}
			if sc["identities:manage"] && len(req.Apps) == 0 && req.AppPrefix == "" {
				return 0, nil, e400("identities:manage exige una lista de Apps (--apps) o un app_prefix: nunca sin límite.")
			}
			if sc["ports:manage"] && !req.AllowPorts {
				return 0, nil, e400("ports:manage es opt-in: añade allow_ports (--allow-ports). Publica servicios TCP/UDP en la IP del servidor, fuera de Cloudflare.")
			}
			if sc["ports:manage"] && len(req.Apps) == 0 && req.AppPrefix == "" {
				return 0, nil, e400("ports:manage exige una lista de Apps (--apps) o un app_prefix: nunca sin límite.")
			}
			if !sc["ports:manage"] && req.AllowPorts {
				return 0, nil, e400("allow_ports solo tiene sentido con el scope ports:manage.")
			}
			if (sc["apps:create"] || sc["members:manage"]) && len(req.AllowedDomains) == 0 {
				return 0, nil, e400("apps:create y members:manage exigen allowed_domains.")
			}
			if (sc["queues:manage"] || sc["runners:manage"] || sc["replication:propose"]) && req.AppPrefix == "" {
				return 0, nil, e400("Los scopes de colas, pools y enlaces exigen app_prefix.")
			}
			var bs []platBinding
			for _, b := range req.Bindings {
				if len(b.Claims) > 8 {
					return 0, nil, e400("Demasiadas condiciones extra en el vínculo.")
				}
				for k, v := range b.Claims {
					if k == "project_path" || k == "ref_protected" || k == "environment" || v == "" || len(v) > 200 || !regexp.MustCompile(`^[a-z_]{1,40}$`).MatchString(k) {
						return 0, nil, e400("Condición de vínculo no válida: «" + k + "».")
					}
				}
				bs = append(bs, platBinding{ProjectPath: b.ProjectPath, Environment: b.Environment, Claims: b.Claims})
			}
			if _, dup := s.plat[req.Name]; dup {
				return 0, nil, e409("Ya existe una cuenta de plataforma con ese nombre (aunque esté revocada: usa otro).")
			}
			a := &platAccount{Name: req.Name, Scopes: sc, Apps: req.Apps, Prefix: req.AppPrefix, Domains: req.AllowedDomains, Expires: s.now().AddDate(0, 0, req.ExpiresInDays), CI: len(req.Bindings) > 0, Bindings: bs}
			pub, priv := newKeyPair()
			k := &key{ID: "k1", Public: pub, Created: s.now()}
			k.Expires = capExpiry(s.now().AddDate(0, 0, 90), a.Expires)
			a.Keys = []*key{k}
			s.plat[a.Name] = a
			return 201, map[string]any{"account": platJSON(a), "key_file": map[string]any{"type": "bifrost_service_account", "api": s.URL, "account": a.Name + "@platform", "private_key_id": k.ID, "private_key": priv}}, nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	a := s.plat[seg[0]]
	if a == nil {
		return 0, nil, e404("No existe esa cuenta de plataforma.")
	}
	switch {
	case len(seg) == 1 && m == http.MethodDelete:
		a.Revoked = true
		return 200, okBody(), nil
	case len(seg) == 2 && seg[1] == "keys" && m == http.MethodPost:
		var req struct {
			ExpiresInDays int `json:"expires_in_days"`
		}
		_ = dec(body, &req)
		if req.ExpiresInDays < 1 || req.ExpiresInDays > 365 {
			return 0, nil, e400("La caducidad es obligatoria: de 1 a 365 días.")
		}
		if a.Revoked {
			return 0, nil, e409("La cuenta está revocada.")
		}
		a.Expires = s.now().AddDate(0, 0, req.ExpiresInDays)
		grace := s.now().Add(time.Hour) // las claves anteriores sobreviven solo la gracia
		for _, old := range a.Keys {
			if !old.Revoked && (old.Expires == nil || old.Expires.After(grace)) {
				old.Expires = &grace
			}
		}
		pub, priv := newKeyPair()
		k := &key{ID: "k" + itoa(len(a.Keys)+1), Public: pub, Created: s.now()}
		k.Expires = capExpiry(s.now().AddDate(0, 0, 90), a.Expires)
		a.Keys = append(a.Keys, k)
		return 201, map[string]any{"account": platJSON(a), "key_file": map[string]any{"type": "bifrost_service_account", "api": s.URL, "account": a.Name + "@platform", "private_key_id": k.ID, "private_key": priv}}, nil
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) verifyPlatformAssertion(name string, parts []string, h struct{ Alg, Kid string }, jti string, exp int64) (identity, bool) {
	a := s.platAcc(name)
	if a == nil || !a.active(s.now()) {
		return identity{}, false
	}
	dec := func(x string) []byte { b, _ := base64.RawURLEncoding.DecodeString(x); return b }
	for _, k := range a.Keys {
		if k.ID == h.Kid && !k.Revoked && (k.Expires == nil || k.Expires.After(s.now())) && ed25519.Verify(k.Public, []byte(parts[0]+"."+parts[1]), dec(parts[2])) {
			if s.jtis[name+"@platform|"+jti] {
				return identity{}, false
			}
			s.jtis[name+"@platform|"+jti] = true
			return identity{Email: "sa:" + name + "@platform", Machine: true, PlatformName: name}, true
		}
	}
	return identity{}, false
}

// CreatePlatformAccount crea una cuenta de plataforma directamente (ayuda de pruebas) y devuelve su archivo de clave en JSON.
func (s *Server) CreatePlatformAccount(name string, scopes []string, apps []string, prefix string, ci bool, domains ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plat == nil {
		s.plat = map[string]*platAccount{}
	}
	sc := map[string]bool{}
	for _, x := range scopes {
		sc[x] = true
		for _, i := range implied[x] {
			sc[i] = true
		}
	}
	a := &platAccount{Name: name, Scopes: sc, Apps: apps, Prefix: prefix, Domains: domains, Expires: s.now().AddDate(0, 0, 30), CI: ci}
	pub, priv := newKeyPair()
	a.Keys = []*key{{ID: "k1", Public: pub, Created: s.now(), Expires: capExpiry(s.now().AddDate(0, 0, 90), a.Expires)}}
	s.plat[name] = a
	b, _ := json.Marshal(map[string]any{"type": "bifrost_service_account", "api": s.URL, "account": name + "@platform", "private_key_id": "k1", "private_key": priv})
	return string(b)
}

func (a *platAccount) domainOK(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at < 0 || strings.HasPrefix(email, "sa:") {
		return false
	}
	d := email[at+1:]
	for _, x := range a.Domains {
		if x == d && d != "platform" {
			return true
		}
	}
	return false
}

func capExpiry(k, account time.Time) *time.Time {
	if k.After(account) {
		k = account
	}
	return &k
}

// ExpireKeysOutside hace caducar todas las claves de una cuenta de plataforma (sin tocar la cuenta): simula claves vencidas.
func (s *Server) ExpireKeysOutside(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.plat[name]; a != nil {
		past := s.now().Add(-time.Hour)
		for _, k := range a.Keys {
			k.Expires = &past
		}
	}
}

// RevokeKeysOutside revoca todas las claves de una cuenta de plataforma.
func (s *Server) RevokeKeysOutside(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.plat[name]; a != nil {
		for _, k := range a.Keys {
			k.Revoked = true
		}
	}
}

// PlatformKeyState devuelve (id, vigente) de cada clave de la cuenta, para comprobar qué claves valen.
func (s *Server) PlatformKeyState(name string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	if a := s.plat[name]; a != nil {
		for _, k := range a.Keys {
			out[k.ID] = !k.Revoked && (k.Expires == nil || k.Expires.After(s.now()))
		}
	}
	return out
}

// PlatformBindingClaims devuelve las condiciones extra del vínculo de CI de una cuenta de plataforma (pruebas).
func (s *Server) PlatformBindingClaims(account string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.plat[account]
	if a == nil || len(a.Bindings) == 0 {
		return nil
	}
	return a.Bindings[0].Claims
}
