package fakeapi

import (
	"net/http"
	"regexp"
	"strings"
)

var queueRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)
var poolRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (q *queue) json() map[string]any {
	o := map[string]any{"kind": q.Kind, "name": q.Name, "display": q.Display, "registered": true, "discovered": false, "hidden": q.Hidden, "counts": nil}
	if q.JS != nil {
		o["jetstream"] = q.JS
	}
	return o
}

func (s *Server) queues_(m string, qs map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, q := range s.queues {
				items = append(items, q.json())
			}
			return 200, map[string]any{"items": items, "redis_available": true, "jetstream_available": true}, nil
		case http.MethodPost:
			var req struct {
				Name, Display, Kind string
				JetStream           map[string]any `json:"jetstream"`
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			if req.Kind == "" {
				req.Kind = "bullmq"
			}
			if req.Kind != "bullmq" && req.Kind != "jetstream" {
				return 0, nil, e400("Tipo de cola desconocido: usa «bullmq» o «jetstream».")
			}
			if !queueRe.MatchString(req.Name) {
				return 0, nil, e400("El nombre de la cola debe empezar con letra o número y usar solo letras, números, «_», «.» o «-» (máximo 80 caracteres; sin «:»).")
			}
			if req.Kind == "bullmq" && req.JetStream != nil {
				return 0, nil, e400("Los parámetros de JetStream solo aplican con kind «jetstream».")
			}
			if req.Kind == "jetstream" {
				js := req.JetStream
				if js == nil {
					return 0, nil, e400("Falta la configuración de JetStream (retención, réplicas, almacenamiento).")
				}
				if r := js["retention"]; r != "workqueue" && r != "limits" {
					return 0, nil, e400("La retención debe ser «workqueue» o «limits».")
				}
				if st := js["storage"]; st != "file" && st != "memory" {
					return 0, nil, e400("El almacenamiento debe ser «file» o «memory».")
				}
				if rp, _ := js["replicas"].(float64); rp != 1 && rp != 3 {
					return 0, nil, e400("Las réplicas deben ser 1 o 3.")
				}
			}
			for _, x := range s.queues {
				if x.Kind == req.Kind && x.Name == req.Name {
					return 0, nil, e409("Esa cola ya está declarada.")
				}
			}
			nq := &queue{Kind: req.Kind, Name: req.Name, Display: req.Display, JS: req.JetStream}
			if nq.Display == "" {
				nq.Display = req.Name
			}
			if nq.JS != nil {
				nq.JS["consumers"], nq.JS["bytes"], nq.JS["messages"] = 0, 0, 0
			}
			s.queues = append(s.queues, nq)
			return 201, nq.json(), nil
		}
		return 0, nil, e404("No existe esa ruta.")
	}
	kind := first(qs, "kind")
	if kind == "" {
		kind = "bullmq"
	}
	for i, x := range s.queues {
		if x.Kind != kind || x.Name != seg[0] {
			continue
		}
		switch m {
		case http.MethodPut:
			var req struct {
				Display *string
				Hidden  *bool
			}
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			if req.Display == nil && req.Hidden == nil {
				return 0, nil, e400("No hay nada que cambiar.")
			}
			if req.Display != nil {
				x.Display = *req.Display
			}
			if req.Hidden != nil {
				x.Hidden = *req.Hidden
			}
			return 200, x.json(), nil
		case http.MethodDelete:
			if first(qs, "confirm") != x.Name && kind == "jetstream" {
				return 0, nil, e400("Para borrar la cola escribe su nombre en la confirmación.")
			}
			s.queues = append(s.queues[:i], s.queues[i+1:]...)
			return 200, okBody(), nil
		}
	}
	return 0, nil, e404("No existe esa cola.")
}

func (p *pool) json() map[string]any {
	return map[string]any{"name": p.Name, "labels": nz(p.Labels), "scope": p.Scope, "min": p.Min, "max": p.Max, "origins": nz(p.Origins), "paused": p.Paused, "app": p.App,
		"machines": 0, "slots": 0, "slots_used": 0, "queued": 0, "month_cost_usd": nil, "pending": true, "observed": false}
}

func (s *Server) runners_(m string, qs map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) == 0 {
		if m != http.MethodGet {
			return 0, nil, e404("No existe esa ruta.")
		}
		ps := []map[string]any{}
		for _, p := range s.pools {
			ps = append(ps, p.json())
		}
		return 200, map[string]any{"orchestrator": map[string]any{"state": "ok"}, "origins": []any{}, "pools": ps, "machines": []any{}, "queue": map[string]int{"queued": 0, "running": 0}}, nil
	}
	if seg[0] != "pools" {
		return 0, nil, e404("No existe esa ruta.")
	}
	if len(seg) == 1 && m == http.MethodPost {
		var req struct {
			Name, Scope, App string
			Labels, Origins  []string
			Min, Max         *int
		}
		if e := dec(body, &req); e != nil {
			return 0, nil, e
		}
		if !poolRe.MatchString(req.Name) {
			return 0, nil, e400("El nombre del pool debe empezar con letra minúscula o número y usar solo minúsculas, números y «-» (máximo 40 caracteres).")
		}
		if req.Scope == "" {
			req.Scope = "project"
		}
		if req.Scope != "project" && req.Scope != "group" && req.Scope != "shared" {
			return 0, nil, e400("El alcance debe ser «project», «group» o «shared».")
		}
		if len(req.Origins) == 0 {
			return 0, nil, e400("Elige al menos un origen de máquinas.")
		}
		mn, mx := 0, 4
		if req.Min != nil {
			mn = *req.Min
		}
		if req.Max != nil {
			mx = *req.Max
		}
		if mn < 0 || mx < 1 || mx > 100 || mn > mx {
			return 0, nil, e400("Los límites no son válidos: el mínimo debe ser 0 o más, el máximo entre 1 y 100, y el mínimo no puede superar al máximo.")
		}
		if req.App != "" && s.apps[req.App] == nil {
			return 0, nil, e400("La App «" + req.App + "» no existe.")
		}
		for _, p := range s.pools {
			if p.Name == req.Name {
				return 0, nil, e409("Ya existe un pool con ese nombre.")
			}
		}
		np := &pool{Name: req.Name, Scope: req.Scope, App: req.App, Labels: req.Labels, Origins: req.Origins, Min: mn, Max: mx}
		s.pools = append(s.pools, np)
		return 201, np.json(), nil
	}
	if len(seg) == 2 {
		for i, p := range s.pools {
			if p.Name != seg[1] {
				continue
			}
			switch m {
			case http.MethodPut:
				var req struct {
					Paused  *bool
					App     *string
					Origins *[]string
					Min     *int
					Max     *int
				}
				if e := dec(body, &req); e != nil {
					return 0, nil, e
				}
				if req.Origins != nil {
					if len(*req.Origins) == 0 {
						return 0, nil, e400("Elige al menos un origen de máquinas.")
					}
					p.Origins = *req.Origins
				}
				if req.Min != nil {
					p.Min = *req.Min
				}
				if req.Max != nil {
					p.Max = *req.Max
				}
				if p.Min < 0 || p.Max < 1 || p.Max > 100 || p.Min > p.Max {
					return 0, nil, e400("Los límites no son válidos: el mínimo debe ser 0 o más, el máximo entre 1 y 100, y el mínimo no puede superar al máximo.")
				}
				if req.Paused != nil {
					p.Paused = *req.Paused
				}
				if req.App != nil {
					if *req.App != "" && s.apps[*req.App] == nil {
						return 0, nil, e400("La App «" + *req.App + "» no existe.")
					}
					p.App = *req.App
				}
				return 200, p.json(), nil
			case http.MethodDelete:
				if first(qs, "confirm") != p.Name {
					return 0, nil, e400("Para borrar el pool escribe su nombre en la confirmación.")
				}
				s.pools = append(s.pools[:i], s.pools[i+1:]...)
				return 200, okBody(), nil
			}
		}
		return 0, nil, e404("No existe ese pool.")
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) replication_(m string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) != 1 && len(seg) != 2 || seg[0] != "links" {
		return 0, nil, e404("No existe esa ruta.")
	}
	if len(seg) == 1 && m == http.MethodPost {
		var req struct{ Name, Source, Destination, Mode, App string }
		if e := dec(body, &req); e != nil {
			return 0, nil, e
		}
		if !poolRe.MatchString(req.Name) {
			return 0, nil, e400("El nombre del enlace debe empezar con letra minúscula o número y usar solo minúsculas, números y «-» (máximo 40 caracteres).")
		}
		if req.Mode == "" {
			req.Mode = "one_way"
		}
		if req.Mode != "one_way" && req.Mode != "two_way" {
			return 0, nil, e400("El modo debe ser «one_way» (origen a destino) o «two_way» (ambos sentidos).")
		}
		if req.Source == "" || req.Destination == "" {
			return 0, nil, e400("Indica la conexión de origen y la de destino.")
		}
		if req.Source == req.Destination {
			return 0, nil, e400("El origen y el destino deben ser conexiones distintas.")
		}
		for _, l := range s.links {
			if l.Name == req.Name {
				return 0, nil, e409("Ya existe un enlace con ese nombre.")
			}
		}
		s.links = append(s.links, &link{Name: req.Name, Source: req.Source, Dest: req.Destination, Mode: req.Mode, App: req.App})
		return 202, map[string]any{"link": req.Name, "version": 1, "state": "pending", "native_url": "https://ratatoskr.example.com/links/" + req.Name}, nil
	}
	if len(seg) == 2 && m == http.MethodGet {
		for _, l := range s.links {
			if l.Name == seg[1] && l.Approved {
				topo := "one_way"
				if l.Mode == "two_way" {
					topo = "two_way"
				}
				return 200, map[string]any{"name": l.Name, "topology": topo, "status": "synced", "center": "", "reasons": []string{}, "connections": []any{}, "flow_list": []any{}}, nil
			}
		}
		return 0, nil, e404("No existe ese enlace en Ratatoskr.")
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) admins_(m string, seg []string, body []byte, id identity) (int, any, *apiErr) {
	adm := func(e string) map[string]any {
		src := "bifrost"
		if e == BootstrapAdmin {
			src = "bootstrap"
		}
		return map[string]any{"email": e, "source": src, "added_by": "x", "added_at": nil}
	}
	if len(seg) == 0 {
		switch m {
		case http.MethodGet:
			items := []map[string]any{}
			for _, e := range s.admins {
				items = append(items, adm(e))
			}
			return 200, map[string]any{"items": items}, nil
		case http.MethodPost:
			var req struct{ Email string }
			if e := dec(body, &req); e != nil {
				return 0, nil, e
			}
			em := strings.ToLower(strings.TrimSpace(req.Email))
			if !emailRe.MatchString(em) {
				return 0, nil, e400("El correo no es válido.")
			}
			for _, e := range s.admins {
				if e == em {
					return 201, adm(em), nil
				}
			}
			s.admins = append(s.admins, em)
			return 201, adm(em), nil
		}
	}
	if len(seg) == 1 && m == http.MethodDelete {
		em := strings.ToLower(seg[0])
		if em == BootstrapAdmin {
			return 0, nil, e400("Es administrador de arranque: se cambia en la configuración de la plataforma.")
		}
		for i, e := range s.admins {
			if e == em {
				s.admins = append(s.admins[:i], s.admins[i+1:]...)
				return 200, okBody(), nil
			}
		}
		return 0, nil, e404("Esa persona no es administradora.")
	}
	return 0, nil, e404("No existe esa ruta.")
}

func (s *Server) people_(m string, seg []string) (int, any, *apiErr) {
	if len(seg) != 1 || m != http.MethodGet {
		return 0, nil, e404("No existe esa ruta.")
	}
	em := strings.ToLower(seg[0])
	isAdmin, src := false, ""
	for _, e := range s.admins {
		if e == em {
			isAdmin, src = true, "bifrost"
			if e == BootstrapAdmin {
				src = "bootstrap"
			}
		}
	}
	apps := []map[string]string{}
	for _, k := range sortedKeys(s.apps) {
		for _, mb := range s.apps[k].Members {
			if mb.Email == em {
				apps = append(apps, map[string]string{"app": k, "role": mb.Role})
			}
		}
	}
	if !isAdmin && len(apps) == 0 {
		return 0, nil, e404("No existe esa persona.")
	}
	return 200, map[string]any{"email": em, "admin": isAdmin, "admin_source": src, "apps": apps}, nil
}
