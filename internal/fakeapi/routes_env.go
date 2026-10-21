package fakeapi

import (
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) envs(id identity, a *app, m string, q map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, e := range a.Envs {
				items = append(items, s.envJSON(a, e))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct {
				Name  string
				Quota *quota
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			en, e := checkSlug(req.Name, "del entorno", 16)
			if e != nil {
				return 0, nil, e
			}
			if s.appStatus(a) == "archived" {
				return 0, nil, e409("La App está archivada.")
			}
			if findEnvIn(a, en) != nil {
				return 0, nil, e409("Ya existe el entorno «" + en + "».")
			}
			if len(a.Envs) >= 8 {
				return 0, nil, e400("La App ya tiene el máximo de entornos (8).")
			}
			eq := a.Quota
			if req.Quota != nil {
				eq = *req.Quota
				if e := validQuota(eq); e != nil {
					return 0, nil, e
				}
			}
			ne := &env{Name: en, Status: "provisioning", Quota: eq, Hosts: map[string]map[string]any{}}
			a.Envs = append(a.Envs, ne)
			if err := s.provision(a, ne); err != nil {
				return 0, nil, err
			}
			return 201, s.envJSON(a, ne), nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	e := findEnvIn(a, seg[0])
	if e == nil {
		return 0, nil, e404("No existe ese entorno.")
	}
	if len(seg) == 1 {
		switch m {
		case http.MethodGet:
			return 200, s.envJSON(a, e), nil
		case http.MethodDelete:
			return s.deleteEnv(id, a, e, q)
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	adm := func() *apiErr {
		if !id.Admin {
			return e403("Solo un administrador puede hacer esto.")
		}
		return nil
	}
	switch seg[1] {
	case "restore", "reconcile":
		if err := s.provision(a, e); err != nil {
			return 0, nil, err
		}
		return 200, s.envJSON(a, e), nil
	case "network":
		if m != http.MethodPut {
			return 200, map[string]any{"routes": []any{}, "services": []any{}, "services_available": true, "traffic_available": false}, nil
		}
		if err := adm(); err != nil {
			return 0, nil, err
		}
		return s.setNetwork(a, e, body)
	case "hosts":
		return s.hostRules(adm, a, e, m, seg[2:], body)
	case "access":
		return s.access(adm, a, e, m, q, body)
	case "scaling":
		return s.scaling(a, e, m, seg[2:], body)
	case "schedule":
		if m != http.MethodGet && !id.Admin && id.PlatformName == "" {
			return 0, nil, e403("Solo un owner de la App o un administrador puede programar la carga.")
		}
		return s.schedule(adm, a, e, m, seg[2:], body)
	case "services":
		return s.envServices(id, a, e, m, q, seg[2:], body)
	case "ports":
		return s.ports(id, a, e, m, q, seg[2:], body)
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) deleteEnv(id identity, a *app, e *env, q map[string][]string) (int, any, *apiErr) {
	if first(q, "purge") != "true" {
		if e.Status != "archived" {
			e.Status, e.ArchivedAt = "archived", s.now()
		}
		return 200, s.envJSON(a, e), nil
	}
	if !id.Admin {
		return 0, nil, e403("Solo un administrador puede hacer esto.")
	}
	if e.Status != "archived" {
		return 0, nil, e409("Archiva el entorno antes de eliminarlo.")
	}
	if first(q, "confirm") != e.Name {
		return 0, nil, e400("Para eliminar escribe el nombre del entorno en «confirm».")
	}
	if s.Retention {
		return 0, nil, e409("El entorno sigue en retención hasta " + s.now().AddDate(0, 0, 30).Format("2006-01-02") + ".")
	}
	if len(e.Services) > 0 {
		return 0, nil, e409("Quita primero las herramientas del entorno (borrarían sus datos).")
	}
	if len(a.Envs) < 2 {
		return 0, nil, e409("Una App necesita al menos un entorno; para quitarlo todo elimina la App.")
	}
	for _, ac := range a.Accounts {
		if len(ac.Envs) == 1 && ac.Envs[0] == e.Name {
			return 0, nil, e409("La cuenta de servicio «" + ac.Name + "» está limitada a este entorno; revócala o cambia su alcance antes.")
		}
	}
	for i, x := range a.Envs {
		if x == e {
			a.Envs = append(a.Envs[:i], a.Envs[i+1:]...)
			break
		}
	}
	return 200, okBody(), nil
}

func (s *Server) setNetwork(a *app, e *env, body []byte) (int, any, *apiErr) {
	var req struct {
		Domains       []string `json:"domains"`
		Internet      bool     `json:"internet"`
		InternetPorts []int    `json:"internet_ports"`
		Egress        *[]egress
	}
	if err := dec(body, &req); err != nil {
		return 0, nil, err
	}
	if e.Status == "archived" {
		return 0, nil, e409("El entorno está archivado; restáuralo antes de cambiar su red.")
	}
	if len(req.Domains) > 10 {
		return 0, nil, e400("Demasiados dominios (máximo 10).")
	}
	var doms []string
	for _, d := range req.Domains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if !strings.Contains(d, ".") || strings.Contains(d, "*") || net.ParseIP(d) != nil || strings.ContainsAny(d, " _/") || strings.HasPrefix(d, "-") {
			return 0, nil, e400("«" + d + "» no es un dominio válido (ej. www.ejemplo.cl).")
		}
		if d == "example.com" || strings.HasSuffix(d, ".example.com") {
			return 0, nil, e400("El dominio «" + d + "» pertenece a la plataforma.")
		}
		for _, o := range doms {
			if o == d {
				return 0, nil, e400("El dominio «" + d + "» está repetido.")
			}
		}
		for _, oa := range s.apps {
			for _, oe := range oa.Envs {
				if oe == e {
					continue
				}
				for _, od := range oe.Network.Domains {
					if od == d {
						return 0, nil, e409("El dominio «" + d + "» ya lo usa " + oa.Name + "/" + oe.Name + ".")
					}
				}
			}
		}
		doms = append(doms, d)
	}
	sort.Strings(doms)
	if len(req.InternetPorts) > 0 && !req.Internet {
		return 0, nil, e400("Indicaste puertos de salida pero la salida a internet está apagada.")
	}
	if len(req.InternetPorts) > 10 {
		return 0, nil, e400("Demasiados puertos de salida (máximo 10).")
	}
	seen := map[int]bool{}
	for _, p := range req.InternetPorts {
		if p < 1 || p > 65535 {
			return 0, nil, e400("Los puertos deben estar entre 1 y 65535.")
		}
		if seen[p] {
			return 0, nil, e400("Puerto repetido: " + itoa(p) + ".")
		}
		seen[p] = true
	}
	eg := e.Network.Egress
	if req.Egress != nil {
		eg = *req.Egress
	}
	for _, r := range eg {
		if _, _, err := net.ParseCIDR(r.CIDR); err != nil || r.Port < 1 || r.Port > 65535 {
			return 0, nil, e400("Destino permitido no válido: «" + r.CIDR + "».")
		}
	}
	if len(eg) > 0 && req.Internet {
		return 0, nil, e400("La salida está abierta a cualquier destino: quita primero la lista de destinos permitidos.")
	}
	ports := append([]int(nil), req.InternetPorts...)
	sort.Ints(ports)
	e.Network = network{Domains: doms, Internet: req.Internet, InternetPorts: ports, Egress: eg}
	return 200, s.envJSON(a, e), nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func (s *Server) hostRules(adm func() *apiErr, a *app, e *env, m string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) != 2 || seg[1] != "rules" {
		return 0, nil, e404("No existe esa ruta.")
	}
	host := seg[0]
	view := func(r map[string]any) map[string]any {
		return map[string]any{"host": host, "rules": r, "routes": map[string]any{"matched": 0, "applied": 0, "shared": []string{}}, "proxy_aware": false, "deny_available": true,
			"your_ip": "", "updated_by": "x", "updated_at": nil}
	}
	switch m {
	case http.MethodGet:
		if r, ok := e.Hosts[host]; ok {
			return 200, view(r), nil
		}
		return 200, view(map[string]any{"ip_allow": []string{}, "ip_deny": []string{}, "ip_notes": map[string]any{}, "rate_limit": nil, "basic_auth": []any{},
			"www": "", "security_headers": false, "compress": false, "max_body_mb": 0, "cors": nil, "https_redirect": false, "timeout_seconds": 0, "maintenance": nil}), nil
	case http.MethodPut:
		if err := adm(); err != nil {
			return 0, nil, err
		}
		var raw map[string]any
		if err := dec(body, &raw); err != nil {
			return 0, nil, err
		}
		// Nunca se devuelven contraseñas.
		ba := []map[string]any{}
		if l, ok := raw["basic_auth"].([]any); ok {
			for _, u := range l {
				if um, ok := u.(map[string]any); ok {
					ba = append(ba, map[string]any{"name": um["name"]})
				}
			}
		}
		raw["basic_auth"] = ba
		if err := s.normHostRules(raw); err != nil {
			return 0, nil, err
		}
		// Como el servidor, PUT REEMPLAZA todo: lo que no viaja queda apagado.
		for k, zero := range map[string]any{"https_redirect": false, "timeout_seconds": 0, "maintenance": nil, "rate_limit": nil, "cors": nil, "www": "", "security_headers": false, "compress": false, "max_body_mb": 0} {
			if _, ok := raw[k]; !ok {
				raw[k] = zero
			}
		}
		e.Hosts[host] = raw
		return 200, view(raw), nil
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) access(adm func() *apiErr, a *app, e *env, m string, q map[string][]string, body []byte) (int, any, *apiErr) {
	list := func() map[string]any {
		in := []map[string]any{}
		for _, r := range e.Access {
			in = append(in, map[string]any{"app": r.From, "hidden": false, "service": r.Service, "port": r.Port, "created_by": "x", "created_at": "2026-01-01T00:00:00Z"})
		}
		return map[string]any{"incoming": in, "outgoing": []any{}}
	}
	switch m {
	case http.MethodGet:
		return 200, list(), nil
	case http.MethodPost:
		if err := adm(); err != nil {
			return 0, nil, err
		}
		var req struct {
			From, Service string
			Port          int
		}
		if err := dec(body, &req); err != nil {
			return 0, nil, err
		}
		if req.From == "" || req.Service == "" || req.Port < 1 || req.Port > 65535 {
			return 0, nil, e400("Indica la App de origen, el servicio de destino y un puerto válido.")
		}
		if req.From == a.Name {
			return 0, nil, e400("Dentro de una misma App los servicios ya se hablan entre sí.")
		}
		src := s.apps[req.From]
		if src == nil {
			return 0, nil, e400("No existe la App de origen «" + req.From + "».")
		}
		if x := findEnvIn(src, e.Name); x == nil || x.Status != "active" {
			return 0, nil, e400("La App «" + req.From + "» no tiene un entorno «" + e.Name + "» activo: el acceso se da entre entornos del mismo nombre.")
		}
		for _, r := range e.Access {
			if r.From == req.From && r.Service == req.Service && r.Port == req.Port {
				return 0, nil, e409("Esa llamada ya está permitida.")
			}
		}
		e.Access = append(e.Access, accessRule{req.From, req.Service, req.Port})
		return 201, list(), nil
	case http.MethodDelete:
		if err := adm(); err != nil {
			return 0, nil, err
		}
		port := 0
		for _, c := range first(q, "port") {
			port = port*10 + int(c-'0')
		}
		for i, r := range e.Access {
			if r.From == first(q, "from") && r.Service == first(q, "service") && r.Port == port {
				e.Access = append(e.Access[:i], e.Access[i+1:]...)
				return 200, list(), nil
			}
		}
		return 0, nil, e404("No existe esa regla.")
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) scaling(a *app, e *env, m string, seg []string, body []byte) (int, any, *apiErr) {
	if m == http.MethodGet && len(seg) == 0 {
		ws := []map[string]any{}
		for _, w := range e.Workloads {
			ws = append(ws, map[string]any{"kind": w.Kind, "name": w.Name, "replicas": w.Replicas, "ready": w.Replicas, "hpa": w.HPA, "vertical": w.Vertical,
				"request": map[string]any{}, "limit": map[string]any{}, "containers": []any{}})
		}
		return 200, map[string]any{"workloads": ws, "quota": nil, "vertical_available": true}, nil
	}
	if m != http.MethodPut || len(seg) != 2 {
		return 0, nil, e404("No existe esa ruta.")
	}
	if seg[0] != "Deployment" && seg[0] != "StatefulSet" {
		return 0, nil, e400("Solo se escalan Deployments y StatefulSets.")
	}
	var w *workload
	for _, x := range e.Workloads {
		if x.Kind == seg[0] && x.Name == seg[1] {
			w = x
		}
	}
	if w == nil {
		return 0, nil, e404("No existe esa carga en el entorno.")
	}
	var raw struct {
		Horizontal map[string]any `json:"horizontal"`
		Vertical   map[string]any `json:"vertical"`
	}
	if err := dec(body, &raw); err != nil {
		return 0, nil, err
	}
	if raw.Horizontal == nil && raw.Vertical == nil {
		return 0, nil, e400("No hay nada que cambiar.")
	}
	num := func(m map[string]any, k string) int { f, _ := m[k].(float64); return int(f) }
	if h := raw.Horizontal; h != nil {
		if w.Kind == "StatefulSet" {
			return 0, nil, e400("Una carga con estado (StatefulSet) no se escala en horizontal.")
		}
		switch h["mode"] {
		case "fixed":
			if n := num(h, "replicas"); n < 1 || n > 50 {
				return 0, nil, e400("Las réplicas fijas van de 1 a 50. Para apagar la carga usa otra acción.")
			}
			w.Replicas, w.HPA = num(h, "replicas"), nil
		case "auto":
			mn, mx := num(h, "min"), num(h, "max")
			if mn < 1 {
				return 0, nil, e400("El mínimo de réplicas es al menos 1.")
			}
			if mx < mn {
				return 0, nil, e400("El máximo de réplicas no puede ser menor que el mínimo.")
			}
			if num(h, "cpu_target") == 0 && num(h, "memory_target") == 0 {
				return 0, nil, e400("Elige al menos un umbral (CPU o memoria) para autoescalar.")
			}
			w.HPA = map[string]any{"name": w.Name, "min": mn, "max": mx, "cpu_target": num(h, "cpu_target"), "memory_target": num(h, "memory_target"), "managed": true,
				"foreign": false, "conditions": []any{}}
			if w.Replicas < mn {
				w.Replicas = mn
			}
		default:
			return 0, nil, e400("Modo no válido: usa «fixed» o «auto».")
		}
	}
	if v := raw.Vertical; v != nil {
		switch v["mode"] {
		case "off":
			w.Vertical = nil
		case "recommend", "auto":
			w.Vertical = map[string]any{"mode": v["mode"], "min": map[string]any{"cpu_m": num(v, "min_cpu_m"), "memory_bytes": num(v, "min_memory_bytes")},
				"max": map[string]any{"cpu_m": num(v, "max_cpu_m"), "memory_bytes": num(v, "max_memory_bytes")}, "recommendations": []any{}, "controlled": []string{}}
		default:
			return 0, nil, e400("Modo vertical no válido: usa «off», «recommend» o «auto».")
		}
	}
	return 200, okBody(), nil
}

func (s *Server) envServices(id identity, a *app, e *env, m string, q map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, v := range e.Services {
				items = append(items, svcJSON(v))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct {
				Service   string
				Options   map[string]string
				Dedicated bool
			}
			if err := dec(body, &req); err != nil {
				return 0, nil, err
			}
			c, ok := s.catalog[req.Service]
			if !ok {
				return 0, nil, e404("No existe esa herramienta.")
			}
			if req.Dedicated && !c.Dedicated {
				return 0, nil, e400("Esa herramienta no admite instancia dedicada.")
			}
			if e.Status != "active" {
				return 0, nil, e409("El entorno no está activo.")
			}
			for k := range req.Options {
				found := false
				for _, o := range c.Options {
					if o["key"] == k {
						found = true
						if o["admin_only"] == true && !id.Admin {
							return 0, nil, e403("Solo los administradores pueden fijar la opción «" + k + "».")
						}
					}
				}
				if !found {
					return 0, nil, e400("Opciones no válidas para esa herramienta: «" + k + "».")
				}
			}
			for _, v := range e.Services {
				if v.Service == req.Service {
					return 0, nil, e409("El entorno ya tiene esa herramienta.")
				}
			}
			sv := &service{Service: req.Service, SecretName: c.Secret, Dedicated: req.Dedicated, Resources: []string{req.Service + "-" + a.Name + "-" + e.Name},
				AddedBy: id.Email, AddedAt: s.now().UTC().Format(time.RFC3339), opts: req.Options}
			e.Services = append(e.Services, sv)
			return 201, svcJSON(sv), nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	var sv *service
	idx := -1
	for i, v := range e.Services {
		if v.Service == seg[0] {
			sv, idx = v, i
		}
	}
	if sv == nil {
		return 0, nil, e404("El entorno no tiene esa herramienta.")
	}
	switch {
	case len(seg) == 1 && m == http.MethodGet:
		return 200, map[string]any{"service": svcJSON(sv), "values": map[string]float64{}, "units": map[string]string{}, "limits": map[string]float64{}}, nil
	case len(seg) == 1 && m == http.MethodDelete:
		if first(q, "confirm") != sv.Service {
			return 0, nil, e400("Para quitar la herramienta escribe su id en «confirm»: se borran los datos del entorno en ella.")
		}
		e.Services = append(e.Services[:idx], e.Services[idx+1:]...)
		return 200, okBody(), nil
	case len(seg) == 2 && seg[1] == "connection" && m == http.MethodGet:
		keys := []map[string]any{{"key": "host", "secret": false, "value": sv.Service + "." + e.Name + ".svc"}, {"key": "port", "secret": false, "value": "5432"},
			{"key": "password", "secret": true}}
		return 200, map[string]any{"secret_name": sv.SecretName, "keys": keys}, nil
	}
	return 0, nil, e404("No existe esa ruta.")
}
