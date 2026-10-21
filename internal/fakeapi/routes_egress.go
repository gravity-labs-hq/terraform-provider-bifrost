package fakeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ---- salidas a internet por workload (bifrost/docs/egress.md del servidor) ----
//
// Mismo modelo de permisos que los puertos (administradores y ports:manage), cuerpo estricto con «rules» obligatorio, versión optimista (409), puertos
// prohibidos (400), el workload debe existir y tope de 5 reglas por entorno.

type egressEntry struct {
	Name, Protocol string
	Port           int
	Kind, Target   string
}

const maxEgressPerEnv = 5

var egressDenied = map[int]bool{20: true, 21: true, 22: true, 23: true, 53: true, 69: true, 111: true, 135: true, 136: true, 137: true, 138: true, 139: true, 161: true, 389: true, 445: true, 514: true,
	873: true, 1433: true, 1521: true, 2049: true, 2375: true, 2376: true, 3306: true, 3389: true, 5432: true, 5900: true, 5901: true, 5902: true, 5903: true, 5904: true, 5905: true, 6379: true,
	9100: true, 9200: true, 11211: true, 27017: true, 2379: true, 2380: true, 6443: true, 6444: true, 8472: true, 9345: true, 51820: true, 51821: true}

func egressVersion(rs []egressEntry) string {
	if len(rs) == 0 {
		return ""
	}
	c := append([]egressEntry(nil), rs...)
	sort.Slice(c, func(i, j int) bool { return c[i].Name < c[j].Name })
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func (s *Server) egressView(e *env) map[string]any {
	rs := append([]egressEntry(nil), e.Egress...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	list := []any{}
	for _, r := range rs {
		list = append(list, map[string]any{"name": r.Name, "protocol": r.Protocol, "port": r.Port, "target": map[string]any{"kind": r.Kind, "name": r.Target},
			"state": "applied", "message": "", "policy": "egress-" + r.Name})
	}
	return map[string]any{"enabled": true, "version": egressVersion(e.Egress), "rules": list, "notices": []string{},
		"limits": map[string]any{"max_per_env": maxEgressPerEnv, "denied": []int{}, "denied_ranges": [][2]int{{10250, 10259}, {30000, 32767}}}}
}

type egressReq struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Target   struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"target"`
}

func validateEgress(in []egressReq) ([]egressEntry, *apiErr) {
	if len(in) > maxEgressPerEnv {
		return nil, e400(fmt.Sprintf("Demasiadas reglas de salida: el máximo por entorno es %d.", maxEgressPerEnv))
	}
	names, keys := map[string]bool{}, map[string]bool{}
	var out []egressEntry
	for i, r := range in {
		pre := fmt.Sprintf("Regla %d: ", i+1)
		if !portNameRE.MatchString(r.Name) {
			return nil, e400(pre + "El nombre «" + r.Name + "» no es válido: minúsculas, números y guiones, empieza con letra, hasta 30 caracteres.")
		}
		if strings.ToLower(strings.TrimSpace(r.Protocol)) != "tcp" {
			return nil, e400(pre + "«" + r.Name + "»: solo se admite el protocolo tcp en las salidas por workload.")
		}
		if r.Port < 1 || r.Port > 65535 {
			return nil, e400(pre + "El puerto de «" + r.Name + "» debe estar entre 1 y 65535.")
		}
		if egressDenied[r.Port] || (r.Port >= 10250 && r.Port <= 10259) || (r.Port >= 30000 && r.Port <= 32767) {
			return nil, e400(fmt.Sprintf("%sEl puerto %d no se puede abrir: es un puerto prohibido.", pre, r.Port))
		}
		kind := strings.ToLower(strings.TrimSpace(r.Target.Kind))
		if kind != "deployment" && kind != "statefulset" {
			return nil, e400(pre + "El destino de «" + r.Name + "» debe ser un deployment o un statefulset.")
		}
		if !workloadNameRE.MatchString(r.Target.Name) {
			return nil, e400(pre + "El nombre del workload de «" + r.Name + "» no es válido.")
		}
		key := fmt.Sprintf("%s/%s:%d", kind, r.Target.Name, r.Port)
		if names[r.Name] {
			return nil, e400("El nombre «" + r.Name + "» está repetido.")
		}
		if keys[key] {
			return nil, e400(fmt.Sprintf("El puerto %d ya está permitido a ese workload en otra regla.", r.Port))
		}
		names[r.Name], keys[key] = true, true
		out = append(out, egressEntry{Name: r.Name, Protocol: "tcp", Port: r.Port, Kind: kind, Target: r.Target.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// egress atiende GET/PUT .../egress y DELETE .../egress/{id}.
func (s *Server) egress(id identity, e *env, m string, q map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	if !id.Admin && id.PlatformName == "" {
		return 0, nil, e403("Solo los administradores pueden realizar esta acción.")
	}
	switch {
	case len(seg) == 0 && m == http.MethodGet:
		return 200, s.egressView(e), nil
	case len(seg) == 0 && m == http.MethodPut:
		var req struct {
			Version string       `json:"version"`
			Rules   *[]egressReq `json:"rules"`
		}
		dc := json.NewDecoder(strings.NewReader(string(body)))
		dc.DisallowUnknownFields()
		if err := dc.Decode(&req); err != nil {
			return 0, nil, e400("Cuerpo inválido.")
		}
		if req.Rules == nil {
			return 0, nil, e400("Falta «rules» (una lista, vacía para revocar todas las salidas).")
		}
		if e.Status == "archived" {
			return 0, nil, e409("El entorno está archivado; restáuralo antes de abrir salidas.")
		}
		list, err := validateEgress(*req.Rules)
		if err != nil {
			return 0, nil, err
		}
		if req.Version != egressVersion(e.Egress) {
			return 0, nil, e409("Las salidas cambiaron desde que las leíste; vuelve a cargarlas.")
		}
		for _, r := range list {
			ok := false
			for _, w := range e.Workloads {
				ok = ok || (strings.EqualFold(w.Kind, r.Kind) && w.Name == r.Target)
			}
			if !ok {
				return 0, nil, e400("El " + r.Kind + " «" + r.Target + "» no existe en el entorno.")
			}
		}
		e.Egress = list
		return 200, s.egressView(e), nil
	case len(seg) == 1 && m == http.MethodDelete:
		if v := first(q, "version"); v != "" && v != egressVersion(e.Egress) {
			return 0, nil, e409("Las salidas cambiaron desde que las leíste; vuelve a cargarlas.")
		}
		var keep []egressEntry
		found := false
		for _, r := range e.Egress {
			if r.Name == seg[0] {
				found = true
				continue
			}
			keep = append(keep, r)
		}
		if !found {
			return 0, nil, e404("No existe esa regla de salida.")
		}
		e.Egress = keep
		return 200, s.egressView(e), nil
	}
	return 0, nil, e404("No existe esa ruta.")
}

// OpenEgressExternally simula que alguien permite una salida fuera de Terraform (deriva).
func (s *Server) OpenEgressExternally(appName, envName, name string, port int, workload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.findEnv(appName, envName); e != nil {
		e.Egress = append(e.Egress, egressEntry{Name: name, Protocol: "tcp", Port: port, Kind: "deployment", Target: workload})
	}
}

// EgressOf devuelve «tcp/puerto nombre» de las salidas de un entorno, ordenadas.
func (s *Server) EgressOf(appName, envName string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if e := s.findEnv(appName, envName); e != nil {
		for _, r := range e.Egress {
			out = append(out, fmt.Sprintf("tcp/%d %s", r.Port, r.Name))
		}
	}
	sort.Strings(out)
	return out
}
