package provider

import (
	"sort"
	"strconv"
	"strings"
)

// Conversión entre la forma declarativa de Terraform (reglas con días y rangos de horas) y la que usa la API (matriz 7 × 24 de ids de perfil).
// Son funciones puras: se prueban sin Terraform.

// dayNames son los días en el orden de la API (0 = lunes).
var dayNames = [7]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func dayIndex(name string) int {
	for i, d := range dayNames {
		if d == name {
			return i
		}
	}
	return -1
}

// weeklyRule es «estos días, de la hora from (incluida) a la hora to (excluida), rige este perfil». Las reglas posteriores pisan a las anteriores.
type weeklyRule struct {
	Days    []string
	From    int
	To      int
	Profile string
}

// hourRange es lo mismo para un solo día (excepciones).
type hourRange struct {
	From    int
	To      int
	Profile string
}

// buildWeek arma la matriz de la API. Lo que ninguna regla cubre queda "" (rige el perfil base). Ignora lo fuera de rango (lo rechaza el validador).
func buildWeek(rules []weeklyRule) [][]string {
	w := make([][]string, 7)
	for d := range w {
		w[d] = make([]string, 24)
	}
	for _, r := range rules {
		for _, name := range r.Days {
			d := dayIndex(name)
			if d < 0 {
				continue
			}
			for h := max(r.From, 0); h < min(r.To, 24); h++ {
				w[d][h] = r.Profile
			}
		}
	}
	return w
}

// buildDay arma las 24 horas de una excepción.
func buildDay(ranges []hourRange) []string {
	h := make([]string, 24)
	for _, r := range ranges {
		for i := max(r.From, 0); i < min(r.To, 24); i++ {
			h[i] = r.Profile
		}
	}
	return h
}

// rangesOfDay descompone 24 horas en tramos contiguos del mismo perfil (las horas vacías no generan tramo).
func rangesOfDay(hours []string) []hourRange {
	var out []hourRange
	for h := 0; h < len(hours) && h < 24; {
		if hours[h] == "" {
			h++
			continue
		}
		e := h
		for e < 24 && e < len(hours) && hours[e] == hours[h] {
			e++
		}
		out = append(out, hourRange{From: h, To: e, Profile: hours[h]})
		h = e
	}
	return out
}

// rulesOfWeek descompone la matriz en reglas canónicas: los días con exactamente los mismos tramos comparten regla. Siempre da el mismo resultado para la
// misma matriz, así que sirve para mostrar un horario cambiado fuera de Terraform.
func rulesOfWeek(week [][]string) []weeklyRule {
	type group struct {
		ranges []hourRange
		days   []string
		first  int
	}
	bySig := map[string]*group{}
	var order []*group
	for d := 0; d < 7 && d < len(week); d++ {
		rs := rangesOfDay(week[d])
		if len(rs) == 0 {
			continue
		}
		var sb strings.Builder
		for _, r := range rs {
			sb.WriteString(r.Profile)
			sb.WriteByte('|')
			sb.WriteString(strconv.Itoa(r.From))
			sb.WriteByte('-')
			sb.WriteString(strconv.Itoa(r.To))
			sb.WriteByte(';')
		}
		g, ok := bySig[sb.String()]
		if !ok {
			g = &group{ranges: rs, first: d}
			bySig[sb.String()] = g
			order = append(order, g)
		}
		g.days = append(g.days, dayNames[d])
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].first < order[j].first })
	var out []weeklyRule
	for _, g := range order {
		for _, r := range g.ranges {
			out = append(out, weeklyRule{Days: g.days, From: r.From, To: r.To, Profile: r.Profile})
		}
	}
	return out
}

func sameMatrix(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for d := range a {
		if len(a[d]) != len(b[d]) {
			return false
		}
		for h := range a[d] {
			if a[d][h] != b[d][h] {
				return false
			}
		}
	}
	return true
}

func sameHours(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
