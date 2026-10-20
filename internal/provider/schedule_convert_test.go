package provider

import (
	"reflect"
	"testing"
)

func TestBuildWeekYReglasCanonicas(t *testing.T) {
	rules := []weeklyRule{
		{Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: 8, To: 20, Profile: "normal"},
		{Days: []string{"sat"}, From: 10, To: 14, Profile: "pico"},
		{Days: []string{"wed"}, From: 12, To: 13, Profile: "pico"}, // una regla posterior pisa a la anterior
	}
	w := buildWeek(rules)
	if w[0][8] != "normal" || w[0][19] != "normal" || w[0][20] != "" || w[0][7] != "" || w[2][12] != "pico" || w[2][13] != "normal" || w[5][10] != "pico" || w[5][13] != "pico" || w[5][14] != "" || w[6][9] != "" {
		t.Fatalf("matriz: %v", w)
	}
	// La descomposición es canónica y reconstruye la misma matriz.
	back := rulesOfWeek(w)
	if !sameMatrix(buildWeek(back), w) {
		t.Fatalf("la descomposición no reconstruye la matriz: %+v", back)
	}
	again := rulesOfWeek(buildWeek(back))
	if !reflect.DeepEqual(back, again) {
		t.Fatalf("no es canónica:\n%+v\n%+v", back, again)
	}
	// Los días con los mismos tramos comparten regla: lun, mar, jue y vie van juntos.
	var shared bool
	for _, r := range back {
		if r.Profile == "normal" && r.From == 8 && r.To == 20 && len(r.Days) == 4 {
			shared = true
		}
	}
	if !shared {
		t.Fatalf("esperaba una regla compartida por 4 días: %+v", back)
	}
}

func TestHorarioVacioYBordes(t *testing.T) {
	if rs := rulesOfWeek(buildWeek(nil)); len(rs) != 0 {
		t.Fatalf("sin reglas no hay nada que decir: %+v", rs)
	}
	w := buildWeek([]weeklyRule{{Days: []string{"sun", "lunes"}, From: -3, To: 99, Profile: "x"}}) // fuera de rango: se acota; día inexistente: se ignora
	for h := 0; h < 24; h++ {
		if w[6][h] != "x" {
			t.Fatalf("domingo completo: %v", w[6])
		}
	}
	if w[0][0] != "" {
		t.Fatal("el día inexistente no escribe nada")
	}
	if len(w) != 7 || len(w[0]) != 24 {
		t.Fatal("siempre 7 × 24")
	}
}

func TestTramosDeUnDia(t *testing.T) {
	h := buildDay([]hourRange{{0, 24, "a"}, {6, 9, "b"}, {20, 22, "c"}})
	rs := rangesOfDay(h)
	want := []hourRange{{0, 6, "a"}, {6, 9, "b"}, {9, 20, "a"}, {20, 22, "c"}, {22, 24, "a"}}
	if !reflect.DeepEqual(rs, want) {
		t.Fatalf("%+v", rs)
	}
	if !sameHours(buildDay(rs), h) {
		t.Fatal("los tramos reconstruyen las horas")
	}
	if len(rangesOfDay(make([]string, 24))) != 0 || len(rangesOfDay(nil)) != 0 {
		t.Fatal("sin horas, sin tramos")
	}
}
