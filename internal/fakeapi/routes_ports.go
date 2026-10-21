package fakeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---- puertos públicos TCP/UDP (bifrost/docs/puertos.md del servidor) ----
//
// El fake reproduce el contrato: solo administradores y cuentas de plataforma con ports:manage (el scope lo comprueba platAuthorize), cuerpo estricto con «ports»
// obligatorio, validación (reservados, nombres, protocolos, CIDR, duplicados), versión optimista (409), colisión entre Apps y entornos (409) y tope por App.

type portEntry struct {
	Name, Protocol string
	Port           int
	Kind, Target   string
	TargetPort     int
	CIDRs          []string
}

const maxPortsPerApp = 10

var (
	portNameRE       = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$`)
	workloadNameRE   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	reservedPorts    = map[int]bool{22: true, 53: true, 80: true, 111: true, 443: true, 2222: true, 2379: true, 2380: true, 6443: true, 8472: true, 9100: true, 9345: true, 10250: true, 10256: true, 10257: true, 10259: true, 51820: true, 51821: true}
	reservedUDPPorts = map[int]bool{19: true, 123: true, 137: true, 161: true, 389: true, 1900: true, 3702: true, 5353: true, 11211: true}
	internalNets     = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "::ffff:0:0/96"}
)

func (p portEntry) key() string { return p.Protocol + "-" + strconv.Itoa(p.Port) }

func portsVersion(ps []portEntry) string {
	if len(ps) == 0 {
		return ""
	}
	c := append([]portEntry(nil), ps...)
	sort.Slice(c, func(i, j int) bool { return c[i].Name < c[j].Name })
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func (s *Server) portsView(e *env) map[string]any {
	ps := append([]portEntry(nil), e.Ports...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	list := []any{}
	for _, p := range ps {
		list = append(list, map[string]any{"name": p.Name, "protocol": p.Protocol, "port": p.Port, "target": map[string]any{"kind": p.Kind, "name": p.Target, "port": p.TargetPort},
			"allow_cidrs": nz(p.CIDRs), "state": "open", "lb_ready": true, "addresses": []string{"203.0.113.10"}, "message": "", "service": "bfp-" + p.Name})
	}
	return map[string]any{"enabled": true, "version": portsVersion(e.Ports), "ports": list, "notices": []string{},
		"limits": map[string]any{"max_per_app": maxPortsPerApp, "max_total": 100, "used_by_app": 0, "reserved": []int{}, "reserved_ranges": [][2]int{{30000, 32767}}, "reserved_udp": []int{}}}
}

func normalizeCIDR(raw string) (string, *apiErr) {
	s := strings.TrimSpace(raw)
	bad := e400("«" + s + "» no es una IP ni un rango válido (ej. 203.0.113.7 o 203.0.113.0/24).")
	var p netip.Prefix
	if strings.Contains(s, "/") {
		q, err := netip.ParsePrefix(s)
		if err != nil {
			return "", bad
		}
		p = q.Masked()
	} else {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			return "", bad
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	min := 8
	if p.Addr().Is6() {
		min = 16
	}
	if p.Bits() < min {
		return "", e400("El rango «" + s + "» es demasiado amplio (mínimo /" + strconv.Itoa(min) + ").")
	}
	for _, n := range internalNets {
		if netip.MustParsePrefix(n).Overlaps(p) {
			return "", e400("El rango «" + s + "» es interno, de loopback o reservado: una lista blanca de un puerto público solo admite direcciones públicas.")
		}
	}
	return p.String(), nil
}

func validatePorts(in []portReq) ([]portEntry, *apiErr) {
	if len(in) > maxPortsPerApp {
		return nil, e400(fmt.Sprintf("Demasiados puertos: el máximo por App es %d.", maxPortsPerApp))
	}
	names, keys := map[string]bool{}, map[string]bool{}
	var out []portEntry
	for i, p := range in {
		pre := fmt.Sprintf("Puerto %d: ", i+1)
		if !portNameRE.MatchString(p.Name) {
			return nil, e400(pre + "El nombre «" + p.Name + "» no es válido: minúsculas, números y guiones, empieza con letra, hasta 30 caracteres.")
		}
		proto := strings.ToLower(strings.TrimSpace(p.Protocol))
		if proto != "tcp" && proto != "udp" {
			return nil, e400(pre + "El protocolo de «" + p.Name + "» debe ser tcp o udp.")
		}
		if p.Port < 1 || p.Port > 65535 {
			return nil, e400(pre + "El puerto público de «" + p.Name + "» debe estar entre 1 y 65535.")
		}
		if reservedPorts[p.Port] || (p.Port >= 30000 && p.Port <= 32767) || (proto == "udp" && reservedUDPPorts[p.Port]) {
			return nil, e400(fmt.Sprintf("%sEl puerto %s/%d no se puede abrir: está reservado por la plataforma.", pre, proto, p.Port))
		}
		kind := strings.ToLower(strings.TrimSpace(p.Target.Kind))
		if kind != "deployment" && kind != "statefulset" {
			return nil, e400(pre + "El destino de «" + p.Name + "» debe ser un deployment o un statefulset.")
		}
		if !workloadNameRE.MatchString(p.Target.Name) {
			return nil, e400(pre + "El nombre del workload de «" + p.Name + "» no es válido.")
		}
		if p.Target.Port < 1 || p.Target.Port > 65535 {
			return nil, e400(pre + "El puerto del contenedor de «" + p.Name + "» debe estar entre 1 y 65535.")
		}
		if len(p.AllowCIDRs) > 50 {
			return nil, e400(pre + "Demasiados rangos (máximo 50).")
		}
		e := portEntry{Name: p.Name, Protocol: proto, Port: p.Port, Kind: kind, Target: p.Target.Name, TargetPort: p.Target.Port}
		seen := map[string]bool{}
		for _, c := range p.AllowCIDRs {
			n, err := normalizeCIDR(c)
			if err != nil {
				return nil, e400(pre + err.msg)
			}
			if !seen[n] {
				seen[n] = true
				e.CIDRs = append(e.CIDRs, n)
			}
		}
		sort.Strings(e.CIDRs)
		if names[e.Name] {
			return nil, e400("El nombre «" + e.Name + "» está repetido.")
		}
		if keys[e.key()] {
			return nil, e400(fmt.Sprintf("El puerto %s/%d está repetido.", e.Protocol, e.Port))
		}
		names[e.Name], keys[e.key()] = true, true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type portReq struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Target   struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		Port int    `json:"port"`
	} `json:"target"`
	AllowCIDRs []string `json:"allow_cidrs"`
}

// savePorts aplica una lista ya validada: versión, destinos, colisiones y tope por App.
func (s *Server) savePorts(a *app, e *env, version string, list []portEntry) *apiErr {
	if version != portsVersion(e.Ports) {
		return e409("Los puertos cambiaron desde que los leíste; vuelve a cargarlos.")
	}
	for _, p := range list {
		ok := false
		for _, w := range e.Workloads {
			ok = ok || (strings.EqualFold(w.Kind, p.Kind) && w.Name == p.Target)
		}
		if !ok {
			return e400("El " + p.Kind + " «" + p.Target + "» no existe en el entorno.")
		}
	}
	perApp := len(list)
	for _, o := range s.apps {
		for _, oe := range o.Envs {
			if o == a && oe == e {
				continue
			}
			if o == a {
				perApp += len(oe.Ports)
			}
			for _, op := range oe.Ports {
				for _, p := range list {
					if op.key() == p.key() {
						return e409(fmt.Sprintf("El puerto %s/%d ya está reservado por otro entorno u otra App.", p.Protocol, p.Port))
					}
				}
			}
		}
	}
	if perApp > maxPortsPerApp {
		return e400(fmt.Sprintf("Se superaría el máximo de %d puertos por App (entre todos sus entornos).", maxPortsPerApp))
	}
	e.Ports = list
	return nil
}

// ports atiende GET/PUT .../ports y DELETE .../ports/{id}.
func (s *Server) ports(id identity, a *app, e *env, m string, q map[string][]string, seg []string, body []byte) (int, any, *apiErr) {
	if !id.Admin && id.PlatformName == "" {
		return 0, nil, e403("Solo los administradores pueden realizar esta acción.")
	}
	switch {
	case len(seg) == 0 && m == http.MethodGet:
		return 200, s.portsView(e), nil
	case len(seg) == 0 && m == http.MethodPut:
		var req struct {
			Version string     `json:"version"`
			Ports   *[]portReq `json:"ports"`
		}
		dc := json.NewDecoder(strings.NewReader(string(body)))
		dc.DisallowUnknownFields()
		if err := dc.Decode(&req); err != nil {
			return 0, nil, e400("Cuerpo inválido.")
		}
		if req.Ports == nil {
			return 0, nil, e400("Falta «ports» (una lista, vacía para cerrar todos los puertos).")
		}
		if e.Status == "archived" {
			return 0, nil, e409("El entorno está archivado; restáuralo antes de abrir puertos.")
		}
		list, err := validatePorts(*req.Ports)
		if err != nil {
			return 0, nil, err
		}
		if err := s.savePorts(a, e, req.Version, list); err != nil {
			return 0, nil, err
		}
		return 200, s.portsView(e), nil
	case len(seg) == 1 && m == http.MethodDelete:
		if v := first(q, "version"); v != "" && v != portsVersion(e.Ports) {
			return 0, nil, e409("Los puertos cambiaron desde que los leíste; vuelve a cargarlos.")
		}
		var keep []portEntry
		found := false
		for _, p := range e.Ports {
			if p.Name == seg[0] {
				found = true
				continue
			}
			keep = append(keep, p)
		}
		if !found {
			return 0, nil, e404("No existe ese puerto.")
		}
		e.Ports = keep
		return 200, s.portsView(e), nil
	}
	return 0, nil, e404("No existe esa ruta.")
}

// OpenPortExternally simula que alguien abre un puerto fuera de Terraform (deriva) sin pasar por ninguna comprobación.
func (s *Server) OpenPortExternally(appName, envName, name, proto string, port int, workload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.apps[appName]; a != nil {
		if e := findEnvIn(a, envName); e != nil {
			e.Ports = append(e.Ports, portEntry{Name: name, Protocol: proto, Port: port, Kind: "deployment", Target: workload, TargetPort: port})
		}
	}
}

// PortsOf devuelve «proto/puerto nombre» de los puertos abiertos de un entorno, ordenados.
func (s *Server) PortsOf(appName, envName string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if a := s.apps[appName]; a != nil {
		if e := findEnvIn(a, envName); e != nil {
			for _, p := range e.Ports {
				out = append(out, fmt.Sprintf("%s/%d %s", p.Protocol, p.Port, p.Name))
			}
		}
	}
	sort.Strings(out)
	return out
}
