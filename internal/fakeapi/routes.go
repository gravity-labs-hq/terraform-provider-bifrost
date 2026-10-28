package fakeapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var emailRe = regexp.MustCompile(`^[^@\s<>,;"]+@[^@\s<>,;"]+\.[a-z]{2,}$`)

func dec(body []byte, v any) *apiErr {
	if len(body) == 0 {
		return nil
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	if err := d.Decode(v); err != nil {
		return e400("Cuerpo no válido.")
	}
	return nil
}

func okBody() map[string]bool { return map[string]bool{"ok": true} }

// route devuelve (código, cuerpo, error).
func (s *Server) route(id identity, r *http.Request, seg []string, body []byte) (int, any, *apiErr) {
	m := r.Method
	q := r.URL.Query()
	admin := func() *apiErr {
		if !id.Admin {
			return e403("Solo un administrador puede hacer esto.")
		}
		return nil
	}
	switch seg[0] {
	case "me":
		return 200, map[string]any{"email": id.Email, "name": id.Email, "roles": []string{}, "is_admin": id.Admin}, nil
	case "services":
		return s.services(seg)
	case "apps":
		return s.apps_(id, m, q, seg[1:], body)
	case "queues":
		if m != http.MethodGet {
			if e := admin(); e != nil {
				return 0, nil, e
			}
		}
		return s.queues_(m, q, seg[1:], body)
	case "runners":
		if m != http.MethodGet {
			if e := admin(); e != nil {
				return 0, nil, e
			}
		}
		return s.runners_(m, q, seg[1:], body)
	case "replication":
		if m != http.MethodGet {
			if e := admin(); e != nil {
				return 0, nil, e
			}
		}
		return s.replication_(m, seg[1:], body)
	case "platform-accounts":
		if e := admin(); e != nil || id.PlatformName != "" {
			return 0, nil, e403("Solo una persona administradora gestiona las cuentas de plataforma.")
		}
		return s.platformAccounts(m, seg[1:], body)
	case "admins", "people":
		if e := admin(); e != nil {
			return 0, nil, e
		}
		if seg[0] == "admins" {
			return s.admins_(m, seg[1:], body, id)
		}
		return s.people_(m, seg[1:])
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) services(seg []string) (int, any, *apiErr) {
	info := func(id string, c catalogEntry) map[string]any {
		return map[string]any{"id": id, "name": c.Name, "description": c.Name, "isolation": c.Isolation, "dedicated_support": c.Dedicated, "secret_name": c.Secret, "options": c.Options, "operations": []any{}}
	}
	if len(seg) == 1 {
		items := []map[string]any{}
		for _, k := range sortedKeys(s.catalog) {
			items = append(items, info(k, s.catalog[k]))
		}
		return 200, map[string]any{"items": items}, nil
	}
	c, ok := s.catalog[seg[1]]
	if !ok {
		return 0, nil, e404("No existe esa herramienta.")
	}
	return 200, info(seg[1], c), nil
}

func (s *Server) getApp(name string) (*app, *apiErr) {
	a := s.apps[name]
	if a == nil {
		return nil, e404("No existe esa App.")
	}
	return a, nil
}

func (s *Server) apps_(id identity, m string, q map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	adminOnly := func() *apiErr {
		if !id.Admin {
			return e403("Solo un administrador puede hacer esto.")
		}
		return nil
	}
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, k := range sortedKeys(s.apps) {
				if pa := s.platAcc(id.PlatformName); id.PlatformName != "" && (pa == nil || !pa.permits(s.apps[k])) {
					continue
				}
				items = append(items, s.appJSON(s.apps[k]))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			if e := adminOnly(); e != nil {
				return 0, nil, e
			}
			return s.createApp(id, body)
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	a, e := s.getApp(seg[0])
	if e != nil {
		return 0, nil, e
	}
	if len(seg) == 1 {
		switch m {
		case http.MethodGet:
			return 200, s.appJSON(a), nil
		case http.MethodPatch:
			var req struct {
				DisplayName *string `json:"display_name"`
				Description *string `json:"description"`
				Group       *string `json:"group"`
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			if req.DisplayName == nil && req.Description == nil && req.Group == nil {
				return 0, nil, e400("No hay nada que cambiar.")
			}
			if s.appStatus(a) == "archived" {
				return 0, nil, e409("La App está archivada.")
			}
			if req.DisplayName != nil {
				if strings.TrimSpace(*req.DisplayName) == "" || len([]rune(*req.DisplayName)) > 80 {
					return 0, nil, e400("El nombre visible es obligatorio, de hasta 80 caracteres y sin saltos de línea.")
				}
				a.Display = strings.TrimSpace(*req.DisplayName)
			}
			if req.Description != nil {
				if len([]rune(*req.Description)) > 200 {
					return 0, nil, e400("La descripción admite hasta 200 caracteres y sin saltos de línea.")
				}
				a.Desc = strings.TrimSpace(*req.Description)
			}
			if req.Group != nil {
				g := strings.TrimSpace(*req.Group)
				if g != "" && !groupRE.MatchString(g) {
					return 0, nil, e400("El grupo debe tener minúsculas, números y guiones (sin guiones al inicio ni al final), hasta 32 caracteres.")
				}
				a.Group = g
			}
			return 200, s.appJSON(a), nil
		case http.MethodDelete:
			if e := adminOnly(); e != nil {
				return 0, nil, e
			}
			return s.deleteApp(a, q)
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	switch seg[1] {
	case "quota":
		if e := adminOnly(); e != nil {
			return 0, nil, e
		}
		var qt quota
		if e := dec(body, &qt); e != nil {
			return 0, nil, e
		}
		if e := validQuota(qt); e != nil {
			return 0, nil, e
		}
		if s.appStatus(a) == "archived" {
			return 0, nil, e409("La App está archivada.")
		}
		a.Quota = qt
		return 200, s.appJSON(a), nil
	case "restore", "reconcile":
		if e := adminOnly(); e != nil {
			return 0, nil, e
		}
		for _, en := range a.Envs {
			if seg[1] == "reconcile" && en.Status == "archived" {
				continue
			}
			if err := s.provision(a, en); err != nil {
				return 0, nil, err
			}
		}
		return 200, s.appJSON(a), nil
	case "links":
		var req struct {
			Pool        *string  `json:"pool"`
			Replication []string `json:"replication"`
		}
		if e := dec(body, &req); e != nil {
			return 0, nil, e
		}
		a.Pool, a.Repl = req.Pool, req.Replication
		return 200, s.appJSON(a), nil
	case "members":
		return s.members(id, a, m, seg[2:], body)
	case "accounts":
		return s.accounts(a, m, seg[2:], body)
	case "bindings":
		return s.bindings(a, m, seg[2:], body)
	case "envs":
		return s.envs(id, a, m, q, seg[2:], body)
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) createApp(id identity, body []byte) (int, any, *apiErr) {
	var req struct {
		Name, Description, Owner string
		Owners                   []string
		Quota                    *quota
		Envs                     []struct {
			Name  string
			Quota *quota
		}
	}
	if e := dec(body, &req); e != nil {
		return 0, nil, e
	}
	name, e := checkSlug(req.Name, "de la App", 30)
	if e != nil {
		return 0, nil, e
	}
	if reserved[name] {
		return 0, nil, e400("El nombre «" + name + "» está reservado.")
	}
	q := quota{"2", "4Gi", 20, 5}
	if req.Quota != nil {
		q = *req.Quota
	}
	if e := validQuota(q); e != nil {
		return 0, nil, e
	}
	var owners []string
	for _, o := range req.Owners {
		o = strings.ToLower(strings.TrimSpace(o))
		if !emailRe.MatchString(o) {
			return 0, nil, e400("Uno de los owners no es un correo válido.")
		}
		owners = append(owners, o)
	}
	if len(owners) == 0 {
		owner := strings.ToLower(strings.TrimSpace(req.Owner))
		if owner == "" {
			owner = defaultOwner
		}
		if !emailRe.MatchString(owner) {
			return 0, nil, e400("Indica el correo del owner de la App (owner).")
		}
		owners = []string{owner}
	}
	if s.apps[name] != nil {
		return 0, nil, e409("Ya existe una App con ese nombre.")
	}
	specs := req.Envs
	if len(specs) == 0 {
		specs = append(specs, struct {
			Name  string
			Quota *quota
		}{Name: "prod"})
	}
	if len(specs) > 8 {
		return 0, nil, e400("Una App admite hasta 8 entornos.")
	}
	a := &app{Name: name, Display: strings.TrimSpace(req.Name), Desc: strings.TrimSpace(req.Description), Quota: q, CreatedBy: id.Email,
		ManagedBy: id.PlatformName}
	for _, o := range owners {
		a.Members = append(a.Members, member{o, "owner"})
	}
	seen := map[string]bool{}
	for _, sp := range specs {
		en, e := checkSlug(sp.Name, "del entorno", 16)
		if e != nil {
			return 0, nil, e
		}
		if seen[en] {
			return 0, nil, e400("Entorno repetido: «" + en + "».")
		}
		seen[en] = true
		eq := q
		if sp.Quota != nil {
			eq = *sp.Quota
			if e := validQuota(eq); e != nil {
				return 0, nil, e
			}
		}
		a.Envs = append(a.Envs, &env{Name: en, Status: "provisioning", Quota: eq, Hosts: map[string]map[string]any{}})
	}
	s.apps[name] = a
	var perr *apiErr
	for _, en := range a.Envs {
		if err := s.provision(a, en); err != nil && perr == nil {
			perr = err
		}
	}
	if perr != nil {
		return 0, nil, perr // la App queda creada, en error (igual que el servidor real)
	}
	return 201, s.appJSON(a), nil
}

func (s *Server) deleteApp(a *app, q map[string][]string) (int, any, *apiErr) {
	purge := first(q, "purge") == "true"
	if !purge {
		for _, en := range a.Envs {
			if en.Status != "archived" {
				en.Status, en.ArchivedAt = "archived", s.now()
			}
		}
		return 200, s.appJSON(a), nil
	}
	if s.appStatus(a) != "archived" {
		return 0, nil, e409("Archiva la App antes de eliminarla.")
	}
	if first(q, "confirm") != a.Name {
		return 0, nil, e400("Para eliminar escribe el nombre de la App en «confirm».")
	}
	for _, en := range a.Envs {
		if s.Retention {
			return 0, nil, e409("El entorno «" + en.Name + "» sigue en retención hasta " + s.now().AddDate(0, 0, 30).Format("2006-01-02") + ".")
		}
		if len(en.Services) > 0 {
			return 0, nil, e409("Quita primero las herramientas de la App (borrarían sus datos).")
		}
	}
	delete(s.apps, a.Name)
	return 200, okBody(), nil
}

func first(q map[string][]string, k string) string {
	if v := q[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (s *Server) members(id identity, a *app, m string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, x := range a.Members {
				items = append(items, map[string]any{"email": x.Email, "role": x.Role, "added_by": "x", "added_at": "2026-01-01T00:00:00Z"})
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct{ Email, Role string }
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			em := strings.ToLower(strings.TrimSpace(req.Email))
			if !emailRe.MatchString(em) {
				return 0, nil, e400("El correo no es válido.")
			}
			if req.Role != "owner" && req.Role != "deploy" && req.Role != "readonly" {
				return 0, nil, e400("El rol del miembro debe ser «owner», «deploy» o «readonly».")
			}
			for _, x := range a.Members {
				if x.Email == em {
					return 0, nil, e409("Esa persona ya es miembro de la App; cambia su rol.")
				}
			}
			a.Members = append(a.Members, member{em, req.Role})
			return 201, map[string]any{"email": em, "role": req.Role}, nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	email := strings.ToLower(seg[0])
	idx := -1
	owners := 0
	for i, x := range a.Members {
		if x.Email == email {
			idx = i
		}
		if x.Role == "owner" {
			owners++
		}
	}
	if idx < 0 {
		return 0, nil, e404("Esa persona no es miembro de la App.")
	}
	switch m {
	case http.MethodPut:
		var req struct{ Role string }
		if e := dec(body, &req); e != nil {
			return 0, nil, e
		}
		if req.Role != "owner" && req.Role != "deploy" && req.Role != "readonly" {
			return 0, nil, e400("El rol del miembro debe ser «owner», «deploy» o «readonly».")
		}
		if a.Members[idx].Role == "owner" && req.Role != "owner" && owners < 2 {
			return 0, nil, e409("La App necesita al menos un owner: nombra a otro antes de cambiar este rol.")
		}
		a.Members[idx].Role = req.Role
		return 200, map[string]any{"email": email, "role": req.Role}, nil
	case http.MethodDelete:
		if a.Members[idx].Role == "owner" && owners < 2 {
			return 0, nil, e409("La App necesita al menos un owner: nombra a otro antes de quitar a este.")
		}
		a.Members = append(a.Members[:idx], a.Members[idx+1:]...)
		return 200, okBody(), nil
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) envScope(a *app, envs []string) ([]string, *apiErr) {
	var out []string
	for _, raw := range envs {
		en := slug(raw)
		if findEnvIn(a, en) == nil {
			return nil, e400("La App no tiene el entorno «" + en + "».")
		}
		out = append(out, en)
	}
	return out, nil
}

func findEnvIn(a *app, name string) *env {
	for _, e := range a.Envs {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func (s *Server) accounts(a *app, m string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, c := range a.Accounts {
				items = append(items, accountJSON(a.Name, c))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct {
				Name, Role    string
				ExpiresInDays int `json:"expires_in_days"`
				Envs          []string
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			name, e := checkSlug(req.Name, "de la cuenta", 30)
			if e != nil {
				return 0, nil, e
			}
			if req.Role != "deploy" && req.Role != "readonly" && req.Role != "manager" {
				return 0, nil, e400("El rol debe ser «deploy», «readonly» o «manager».")
			}
			if req.Role == "manager" && len(req.Envs) > 0 {
				return 0, nil, e400("El rol manager administra la App entera: no admite entornos concretos (déjalos vacíos).")
			}
			if s.appStatus(a) == "archived" {
				return 0, nil, e409("La App está archivada.")
			}
			envs, e := s.envScope(a, req.Envs)
			if e != nil {
				return 0, nil, e
			}
			for _, c := range a.Accounts { // incluye revocadas: igual que el servidor real
				if c.Name == name {
					return 0, nil, e409("Ya existe una cuenta con ese nombre en la App.")
				}
			}
			pub, priv := newKeyPair()
			k := &key{ID: "k1", Public: pub, Created: s.now()}
			if req.ExpiresInDays > 0 {
				t := s.now().AddDate(0, 0, req.ExpiresInDays)
				k.Expires = &t
			}
			c := &account{Name: name, Role: req.Role, Envs: envs, Keys: []*key{k}}
			a.Accounts = append(a.Accounts, c)
			return 201, map[string]any{"account": accountJSON(a.Name, c), "key_file": s.keyFile(a.Name, c, k, priv)}, nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	var c *account
	for _, x := range a.Accounts {
		if x.Name == seg[0] {
			c = x
		}
	}
	if c == nil {
		return 0, nil, e404("No existe esa cuenta.")
	}
	switch {
	case len(seg) == 1 && m == http.MethodDelete:
		c.Revoked = true
		for _, k := range c.Keys {
			k.Revoked = true
		}
		return 200, okBody(), nil
	case len(seg) == 2 && seg[1] == "keys" && m == http.MethodPost:
		if c.Revoked {
			return 0, nil, e409("La cuenta está revocada.")
		}
		var req struct {
			ExpiresInDays int  `json:"expires_in_days"`
			KeepPrevious  bool `json:"keep_previous"`
		}
		if e := dec(body, &req); e != nil {
			return 0, nil, e
		}
		active := 0
		for _, k := range c.Keys {
			if !k.Revoked && (k.Expires == nil || k.Expires.After(s.now())) {
				active++
			}
		}
		if req.KeepPrevious && active >= 5 { // rotar no se limita aquí: el recurso efímero rota en cada plan
			return 0, nil, e409("La cuenta ya tiene 5 llaves vigentes; revoca alguna antes de crear otra.")
		}
		if !req.KeepPrevious { // rotar: las anteriores vencen en la gracia
			grace := s.now().Add(time.Hour)
			for _, k := range c.Keys {
				if !k.Revoked && (k.Expires == nil || k.Expires.After(grace)) {
					k.Expires = &grace
				}
			}
		}
		pub, priv := newKeyPair()
		k := &key{ID: "k" + strconv.Itoa(len(c.Keys)+1), Public: pub, Created: s.now()}
		if req.ExpiresInDays > 0 {
			t := s.now().AddDate(0, 0, req.ExpiresInDays)
			k.Expires = &t
		}
		c.Keys = append(c.Keys, k)
		return 201, map[string]any{"key_file": s.keyFile(a.Name, c, k, priv)}, nil
	case len(seg) == 3 && seg[1] == "keys" && m == http.MethodDelete:
		for _, k := range c.Keys {
			if k.ID == seg[2] {
				if k.Revoked {
					return 0, nil, e409("La llave ya está revocada.")
				}
				k.Revoked = true
				return 200, okBody(), nil
			}
		}
		return 0, nil, e404("No existe esa llave.")
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) bindings(a *app, m string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, b := range a.Bindings {
				items = append(items, bindingJSON(b))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct {
				Kind, Issuer string
				ProjectPath  string `json:"project_path"`
				RefProtected bool   `json:"ref_protected"`
				Claims       map[string]string
				Role         string
				Envs         []string
				Branches     []string
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			if req.Kind == "" {
				req.Kind = "gitlab"
			}
			if req.Kind != "gitlab" {
				return 0, nil, e400("Solo se admite el tipo «gitlab».")
			}
			req.Issuer = strings.TrimRight(strings.TrimSpace(req.Issuer), "/")
			if req.Issuer == "" {
				req.Issuer = "https://gitlab.com"
			}
			if req.Issuer != "https://gitlab.com" {
				return 0, nil, e400("Ese emisor de CI no está permitido en este cluster.")
			}
			p := req.ProjectPath
			if p == "" || !strings.Contains(p, "/") || strings.ContainsAny(p, " ?#") || strings.Contains(p, "..") {
				return 0, nil, e400("El project_path no es válido (ej. grupo/repo).")
			}
			if req.Role != "deploy" && req.Role != "readonly" && req.Role != "manager" {
				return 0, nil, e400("El rol debe ser «deploy», «readonly» o «manager».")
			}
			if e := managerBindingFake(req.Role, req.ProjectPath, req.RefProtected, req.Claims, req.Branches, req.Envs); e != nil {
				return 0, nil, e
			}
			if len(req.Claims) > 8 {
				return 0, nil, e400("Demasiadas condiciones extra (máximo 8).")
			}
			for _, o := range s.apps {
				for _, b := range o.Bindings {
					if b.Issuer == req.Issuer && b.ProjectPath == p {
						return 0, nil, e409("Ese repo ya está vinculado a la App «" + o.Name + "».")
					}
				}
			}
			envs, e := s.envScope(a, req.Envs)
			if e != nil {
				return 0, nil, e
			}
			branches, e := validBranches(req.Branches)
			if e != nil {
				return 0, nil, e
			}
			b := &binding{ID: s.id(), Kind: req.Kind, Issuer: req.Issuer, ProjectPath: p, RefProtected: req.RefProtected, Claims: req.Claims, Role: req.Role, Envs: envs, Branches: branches}
			a.Bindings = append(a.Bindings, b)
			return 201, bindingJSON(b), nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	if m == http.MethodPut { // edición en sitio: reemplaza proyecto, ramas, condiciones, entornos y rol; el tipo y el emisor no cambian
		var cur *binding
		for _, b := range a.Bindings {
			if b.ID == seg[0] {
				cur = b
			}
		}
		if cur == nil {
			return 0, nil, e404("No existe ese vínculo.")
		}
		var req struct {
			Kind, Issuer string
			ProjectPath  string `json:"project_path"`
			RefProtected bool   `json:"ref_protected"`
			Claims       map[string]string
			Role         string
			Envs         []string
			Branches     []string
		}
		if e := dec(body, &req); e != nil {
			return 0, nil, e
		}
		if iss := strings.TrimRight(strings.TrimSpace(req.Issuer), "/"); (req.Kind != "" && req.Kind != cur.Kind) || (iss != "" && iss != cur.Issuer) {
			return 0, nil, e400("El tipo y el emisor de un vínculo no se pueden cambiar; quítalo y vincula de nuevo.")
		}
		p := req.ProjectPath
		if p == "" || !strings.Contains(p, "/") || strings.ContainsAny(p, " ?#") || strings.Contains(p, "..") {
			return 0, nil, e400("El project_path no es válido (ej. grupo/repo).")
		}
		if req.Role != "deploy" && req.Role != "readonly" && req.Role != "manager" {
			return 0, nil, e400("El rol debe ser «deploy», «readonly» o «manager».")
		}
		if e := managerBindingFake(req.Role, req.ProjectPath, req.RefProtected, req.Claims, req.Branches, req.Envs); e != nil {
			return 0, nil, e
		}
		if len(req.Claims) > 8 {
			return 0, nil, e400("Demasiadas condiciones extra (máximo 8).")
		}
		for _, o := range s.apps {
			for _, b := range o.Bindings {
				if b != cur && b.Issuer == cur.Issuer && b.ProjectPath == p {
					return 0, nil, e409("Ese repo ya está vinculado a la App «" + o.Name + "».")
				}
			}
		}
		envs, e := s.envScope(a, req.Envs)
		if e != nil {
			return 0, nil, e
		}
		branches, e := validBranches(req.Branches)
		if e != nil {
			return 0, nil, e
		}
		cur.ProjectPath, cur.RefProtected, cur.Claims, cur.Role, cur.Envs, cur.Branches = p, req.RefProtected, req.Claims, req.Role, envs, branches
		return 200, bindingJSON(cur), nil
	}
	if m == http.MethodDelete {
		for i, b := range a.Bindings {
			if b.ID == seg[0] {
				a.Bindings = append(a.Bindings[:i], a.Bindings[i+1:]...)
				return 200, okBody(), nil
			}
		}
		return 0, nil, e404("No existe ese vínculo.")
	}
	return 0, nil, e404("No existe esa ruta.")
}

var (
	groupRE  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+\*?$`)
)

// validBranches replica la validación del servidor: nombres exactos o con un «*» final, sin repetidos, hasta 20.
func validBranches(in []string) ([]string, *apiErr) {
	var out []string
	for _, raw := range in {
		b := strings.TrimSpace(raw)
		if b == "" || b == "*" {
			return nil, e400("Una rama no puede estar vacía ni ser «*»; para permitir cualquier rama deja la lista vacía.")
		}
		if len(b) > 100 || !branchRE.MatchString(b) || strings.Contains(b, "..") || strings.Contains(b, "//") || strings.HasPrefix(b, "/") || strings.HasPrefix(b, "-") || strings.HasSuffix(b, "/") {
			return nil, e400("La rama «" + b + "» no es válida (letras, números, «.», «_», «-» y «/»; un «*» solo al final).")
		}
		dup := false
		for _, x := range out {
			dup = dup || x == b
		}
		if !dup {
			out = append(out, b)
		}
	}
	if len(out) > 20 {
		return nil, e400("Demasiadas ramas (máximo 20).")
	}
	return out, nil
}

// managerBindingFake repite las condiciones obligatorias de un vínculo manager del servidor real (apps.managerBindingRules).
func managerBindingFake(role, project string, refProtected bool, claims map[string]string, branches, envs []string) *apiErr {
	if role != "manager" {
		return nil
	}
	if !refProtected || strings.TrimSpace(claims["environment"]) == "" || strings.Contains(project, "*") {
		return e400("Un vínculo manager exige ref_protected, la condición environment y un proyecto exacto (sin comodines).")
	}
	if len(branches) == 0 {
		return e400("Un vínculo manager exige al menos una rama explícita (branches).")
	}
	for _, b := range branches {
		if strings.Contains(b, "*") {
			return e400("Las ramas de un vínculo manager son nombres exactos, sin «*».")
		}
	}
	if len(envs) > 0 {
		return e400("El rol manager administra la App entera: no admite entornos concretos (déjalos vacíos).")
	}
	return nil
}
