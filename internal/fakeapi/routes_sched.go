package fakeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- programación de carga (docs/programacion.md del servidor) ----

type scheduleState struct {
	spec    map[string]any // lo guardado, ya normalizado
	version int
}

var (
	profileIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	colorRE     = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	profileIcon = map[string]bool{"sun": true, "bolt": true, "moon": true, "power": true, "shield": true, "star": true, "cloud": true}
)

func (s *Server) scheduleView(e *env) map[string]any {
	spec := map[string]any{"enabled": false, "timezone": "America/Santiago", "base": "", "profiles": []any{}, "weekly": emptyWeek(), "exceptions": []any{}}
	configured := e.Schedule != nil
	if configured {
		spec = e.Schedule.spec
	}
	return map[string]any{"configured": configured, "enabled": spec["enabled"], "version": fmt.Sprintf("v%d", versionOf(e)), "schedule": spec, "override": nil, "can_edit": true,
		"workloads": []any{}, "current": nil, "next": []any{}, "capacity": map[string]any{"loads": map[string]any{}, "blocked": []string{}, "warnings": []string{}}}
}

func versionOf(e *env) int {
	if e.Schedule == nil {
		return 0
	}
	return e.Schedule.version
}

func emptyWeek() [][]string {
	w := make([][]string, 7)
	for d := range w {
		w[d] = make([]string, 24)
	}
	return w
}

// schedule atiende GET/PUT .../schedule. Valida como el servidor (BuildSchedule): perfiles, horario 7×24, excepciones y cargas que existen.
func (s *Server) schedule(adm func() *apiErr, a *app, e *env, m string, seg []string, body []byte) (int, any, *apiErr) {
	if len(seg) != 0 {
		return 0, nil, e404("No existe esa ruta.")
	}
	switch m {
	case http.MethodGet:
		return 200, s.scheduleView(e), nil
	case http.MethodPut:
		if e.Status != "active" {
			return 0, nil, e409("El entorno todavía no está listo.")
		}
		var req struct {
			Version  string `json:"version"`
			Enabled  bool   `json:"enabled"`
			Timezone string `json:"timezone"`
			Base     string `json:"base"`
			Profiles []struct {
				ID, Name, Description, Color, Icon string
				Workloads                          map[string]struct {
					Min       int   `json:"min"`
					Max       int   `json:"max"`
					CPUm      int64 `json:"cpu_m"`
					MemMiB    int64 `json:"memory_mib"`
					LimCPUm   int64 `json:"cpu_limit_m"`
					LimMemMiB int64 `json:"memory_limit_mib"`
				}
			} `json:"profiles"`
			Weekly     [][]string `json:"weekly"`
			Exceptions []struct {
				Date, Label string
				Hours       []string
			} `json:"exceptions"`
		}
		dc := json.NewDecoder(strings.NewReader(string(body)))
		dc.DisallowUnknownFields()
		if err := dc.Decode(&req); err != nil {
			return 0, nil, e400("Cuerpo no válido.")
		}
		if req.Version != "" && req.Version != fmt.Sprintf("v%d", versionOf(e)) {
			return 0, nil, e409("La programación cambió desde que la leíste.")
		}
		if len(req.Weekly) != 7 {
			return 0, nil, e400("El horario semanal debe tener 7 días.")
		}
		for _, row := range req.Weekly {
			if len(row) != 24 {
				return 0, nil, e400("Cada día del horario semanal debe tener 24 horas.")
			}
		}
		tz := strings.TrimSpace(req.Timezone)
		if tz == "" {
			tz = "America/Santiago"
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return 0, nil, e400("La zona horaria «" + tz + "» no es válida (ej. America/Santiago).")
		}
		if len(req.Profiles) == 0 || len(req.Profiles) > 12 {
			return 0, nil, e400("Define entre 1 y 12 perfiles.")
		}
		ids := map[string]bool{}
		names := map[string]bool{}
		profs := []any{}
		for _, p := range req.Profiles {
			id := strings.TrimSpace(p.ID)
			if !profileIDRE.MatchString(id) {
				return 0, nil, e400("El identificador de perfil «" + id + "» no es válido (minúsculas, números y guiones; empieza con letra; hasta 24).")
			}
			if ids[id] {
				return 0, nil, e400("Perfil repetido: «" + id + "».")
			}
			ids[id] = true
			name := strings.TrimSpace(p.Name)
			if name == "" || utf8.RuneCountInString(name) > 40 {
				return 0, nil, e400("El nombre del perfil es obligatorio y de hasta 40 caracteres.")
			}
			if names[strings.ToLower(name)] {
				return 0, nil, e400("Hay dos perfiles con el nombre «" + name + "».")
			}
			names[strings.ToLower(name)] = true
			if p.Color != "" && !colorRE.MatchString(p.Color) {
				return 0, nil, e400("El color del perfil «" + name + "» debe ser #RRGGBB.")
			}
			if p.Icon != "" && !profileIcon[p.Icon] {
				return 0, nil, e400("El icono del perfil «" + name + "» no es válido.")
			}
			wls := map[string]any{}
			for w, st := range p.Workloads {
				if !hasWorkload(e, w) {
					return 0, nil, e400("Perfil «" + name + "»: la carga «" + w + "» no existe en este entorno.")
				}
				switch {
				case st.Min < 0 || st.Max < 0:
					return 0, nil, e400("Perfil «" + name + "», carga «" + w + "»: las réplicas no pueden ser negativas.")
				case st.Max < st.Min:
					return 0, nil, e400("Perfil «" + name + "», carga «" + w + "»: el máximo de réplicas no puede ser menor que el mínimo.")
				case st.Max > 0 && st.Min < 1:
					return 0, nil, e400("Perfil «" + name + "», carga «" + w + "»: el mínimo es al menos 1 (para apagar la carga pon mínimo y máximo en 0).")
				}
				wls[w] = map[string]any{"min": st.Min, "max": st.Max, "cpu_m": st.CPUm, "memory_mib": st.MemMiB, "cpu_limit_m": st.LimCPUm, "memory_limit_mib": st.LimMemMiB}
			}
			profs = append(profs, map[string]any{"id": id, "name": name, "description": strings.TrimSpace(p.Description), "color": strings.ToLower(p.Color), "icon": p.Icon, "workloads": wls})
		}
		base := strings.TrimSpace(req.Base)
		if !ids[base] {
			return 0, nil, e400("El perfil base no existe.")
		}
		for d := range req.Weekly {
			for h, p := range req.Weekly[d] {
				if p != "" && !ids[p] {
					return 0, nil, e400(fmt.Sprintf("El horario del día %d %02d:00 usa un perfil que no existe.", d, h))
				}
			}
		}
		today := s.now().UTC().Truncate(24 * time.Hour)
		excs := []map[string]any{}
		seen := map[string]bool{}
		for _, x := range req.Exceptions {
			d, err := time.Parse("2006-01-02", x.Date)
			if err != nil || d.Format("2006-01-02") != x.Date {
				return 0, nil, e400("La fecha de una excepción debe ser AAAA-MM-DD.")
			}
			if d.Before(today.AddDate(0, 0, -2)) {
				continue // ya pasó: el servidor la descarta al guardar
			}
			if d.After(today.AddDate(0, 0, 400)) {
				return 0, nil, e400("Una excepción no puede estar a más de 400 días.")
			}
			if len(x.Hours) != 24 {
				return 0, nil, e400("Cada excepción debe tener 24 horas.")
			}
			if seen[x.Date] {
				return 0, nil, e400("Hay dos excepciones el " + x.Date + ".")
			}
			seen[x.Date] = true
			has := false
			for _, p := range x.Hours {
				if p != "" && !ids[p] {
					return 0, nil, e400("La excepción del " + x.Date + " usa un perfil que no existe.")
				}
				has = has || p != ""
			}
			if !has {
				continue // una excepción sin horas no hace nada
			}
			excs = append(excs, map[string]any{"date": x.Date, "label": strings.TrimSpace(x.Label), "hours": x.Hours})
		}
		sort.Slice(excs, func(i, j int) bool { return excs[i]["date"].(string) < excs[j]["date"].(string) })
		exOut := make([]any, len(excs))
		for i, x := range excs {
			exOut[i] = x
		}
		e.Schedule = &scheduleState{version: versionOf(e) + 1, spec: map[string]any{"enabled": req.Enabled, "timezone": tz, "base": base, "profiles": profs, "weekly": req.Weekly, "exceptions": exOut}}
		return 200, s.scheduleView(e), nil
	}
	return 0, nil, e404("No existe esa ruta.")
}

func hasWorkload(e *env, name string) bool {
	for _, w := range e.Workloads {
		if w.Kind == "Deployment" && w.Name == name {
			return true
		}
	}
	return false
}

// ---- reglas por dirección: normalización como el servidor ----

// normIP replica NormalizeIPEntry: una IP suelta pasa a /32 o /128, un rango se enmascara y 0.0.0.0/0 se rechaza.
func normIP(raw string) (string, *apiErr) {
	t := strings.TrimSpace(raw)
	bad := e400("«" + t + "» no es una IP ni un rango válido (ej. 203.0.113.7 o 203.0.113.0/24).")
	if t == "" || len(t) > 64 {
		return "", bad
	}
	if strings.Contains(t, "/") {
		p, err := netip.ParsePrefix(t)
		if err != nil {
			return "", bad
		}
		p = p.Masked()
		if p.Bits() == 0 {
			return "", e400("El rango «" + t + "» abre todo internet; deja la lista vacía si no quieres restringir por IP.")
		}
		return p.String(), nil
	}
	a, err := netip.ParseAddr(t)
	if err != nil || a.Zone() != "" {
		return "", bad
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()).String(), nil
}

func normIPList(in any) ([]string, *apiErr) {
	out := []string{}
	seen := map[string]bool{}
	l, _ := in.([]any)
	for _, v := range l {
		p, e := normIP(fmt.Sprint(v))
		if e != nil {
			return nil, e
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// normHostRules valida y normaliza el cuerpo de PUT .../rules con lo nuevo de la ola 1 (https_redirect, timeout_seconds, maintenance, ip_notes).
func (s *Server) normHostRules(raw map[string]any) *apiErr {
	allow, e := normIPList(raw["ip_allow"])
	if e != nil {
		return e
	}
	deny, e := normIPList(raw["ip_deny"])
	if e != nil {
		return e
	}
	raw["ip_allow"], raw["ip_deny"] = allow, deny
	in := func(p string) bool {
		for _, l := range [][]string{allow, deny} {
			for _, x := range l {
				if x == p {
					return true
				}
			}
		}
		return false
	}
	notes := map[string]any{}
	if nm, ok := raw["ip_notes"].(map[string]any); ok {
		for k, v := range nm {
			key, e := normIP(k)
			if e != nil || !in(key) {
				continue // nota de una entrada que ya no está
			}
			note := ""
			if vm, ok := v.(map[string]any); ok {
				note, _ = vm["note"].(string)
			}
			note = strings.TrimSpace(note)
			if utf8.RuneCountInString(note) > 120 {
				return e400("La nota de «" + key + "» es demasiado larga (máximo 120 caracteres).")
			}
			notes[key] = map[string]any{"note": note, "by": "x", "at": "2026-01-01T00:00:00Z"} // quién y cuándo los pone el servidor
		}
	}
	raw["ip_notes"] = notes
	if t, ok := raw["timeout_seconds"].(float64); ok && (t < 0 || t > 3600) {
		return e400("El tiempo de espera debe estar entre 1 y 3600 segundos (0 = el predeterminado).")
	}
	if mm, ok := raw["maintenance"].(map[string]any); ok {
		html, _ := mm["html"].(string)
		if len(html) > 4096 || !utf8.ValidString(html) || strings.ContainsRune(html, 0) {
			return e400("El HTML de la página de mantenimiento debe ser texto válido de hasta 4096 bytes.")
		}
		if r, ok := mm["retry_after_seconds"].(float64); ok && (r < 0 || r > 86400) {
			return e400("El tiempo de reintento de la página de mantenimiento va de 0 a 86400 segundos.")
		}
		ips, e := normIPList(mm["allow_ips"])
		if e != nil {
			return e
		}
		if len(ips) > 50 {
			return e400("Demasiadas IPs en la página de mantenimiento (máximo 50).")
		}
		mm["allow_ips"] = ips
	}
	return nil
}
