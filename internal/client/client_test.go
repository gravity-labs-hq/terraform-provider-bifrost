package client_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/client"
	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

func newClient(t *testing.T, srv *fakeapi.Server, auth client.Authenticator, logf func(string, ...any)) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{API: srv.URL, Auth: auth, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startFake(t *testing.T) *fakeapi.Server {
	s := fakeapi.New()
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func TestCheckBase(t *testing.T) {
	ok := map[string]string{
		"https://plane.example.com":  "https://plane.example.com",
		"https://plane.example.com/": "https://plane.example.com",
		"plane.example.com":          "https://plane.example.com",
		"http://127.0.0.1:8080":      "http://127.0.0.1:8080",
		"http://localhost:9":         "http://localhost:9",
	}
	for in, want := range ok {
		got, err := client.CheckBase(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v (quería %q)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "http://plane.example.com", "https://u:p@plane.example.com", "https://x.com?a=1", "https://"} {
		if _, err := client.CheckBase(bad); err == nil {
			t.Errorf("%q debe rechazarse", bad)
		}
	}
}

func TestStaticTokenYErrores(t *testing.T) {
	s := startFake(t)
	c := newClient(t, s, client.StaticToken(fakeapi.DirectAdminTok), nil)
	me, err := c.Me(context.Background())
	if err != nil || !me.IsAdmin {
		t.Fatalf("me=%+v err=%v", me, err)
	}
	bad := newClient(t, s, client.StaticToken("malo"), nil)
	_, err = bad.Me(context.Background())
	var ae *client.APIError
	if !errors.As(err, &ae) || ae.Status != 401 || !strings.Contains(err.Error(), "Token no válido") {
		t.Fatalf("esperaba 401 con mensaje útil: %v", err)
	}
	_, err = c.GetApp(context.Background(), "no-existe")
	if !client.IsNotFound(err) || !strings.Contains(err.Error(), "No existe esa App") {
		t.Fatalf("404: %v", err)
	}
	if _, err = c.CreateApp(context.Background(), client.CreateAppReq{Name: "tienda"}); err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateApp(context.Background(), client.CreateAppReq{Name: "tienda"})
	if !client.IsConflict(err) || !strings.Contains(err.Error(), "Ya existe") {
		t.Fatalf("409: %v", err)
	}
	_, err = c.CreateApp(context.Background(), client.CreateAppReq{Name: "1malo"})
	if err == nil || !strings.Contains(err.Error(), "debe empezar con una letra") {
		t.Fatalf("400: %v", err)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestCanjeCuentaDeServicio(t *testing.T) {
	s := startFake(t)
	admin := newClient(t, s, client.StaticToken(fakeapi.DirectAdminTok), nil)
	ctx := context.Background()
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: "tienda"}); err != nil {
		t.Fatal(err)
	}
	acc, err := admin.CreateAccount(ctx, "tienda", client.CreateAccountReq{Name: "deploy", Role: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := client.ParseKeyJSON(mustJSON(acc.KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	var mu sync.Mutex
	logf := func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() }
	sa := newClient(t, s, &client.KeyAuth{Key: key}, logf)
	me, err := sa.Me(ctx)
	if err != nil || me.IsAdmin || me.Email != "sa:deploy@app-tienda" {
		t.Fatalf("me=%+v err=%v", me, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := sa.GetApp(ctx, "tienda"); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.RequestCount("POST /auth"); n != 0 { // /auth/token no es /v1: no se cuenta ahí
		t.Fatalf("n=%d", n)
	}
	_, err = sa.CreateApp(ctx, client.CreateAppReq{Name: "otra"})
	if !client.IsForbidden(err) {
		t.Fatalf("esperaba 403: %v", err)
	}
	mu.Lock()
	all := strings.Join(logs, "\n")
	mu.Unlock()
	if all == "" {
		t.Fatal("el cliente debería loguear método y ruta")
	}
	for _, secret := range []string{key.PrivateKey, "PRIVATE KEY", "Bearer", fakeapi.DirectAdminTok, key.PrivateKeyID} {
		if strings.Contains(all, secret) {
			t.Fatalf("el log contiene un secreto %q:\n%s", secret, all)
		}
	}
	if err := admin.RevokeAccount(ctx, "tienda", "deploy"); err != nil {
		t.Fatal(err)
	}
	sa2 := newClient(t, s, &client.KeyAuth{Key: key}, nil)
	_, err = sa2.Me(ctx)
	if err == nil || !strings.Contains(err.Error(), "plane rechazó la credencial") || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("revocada: %v", err)
	}
}

func TestKeyInvalida(t *testing.T) {
	for name, in := range map[string]string{"no json": "{", "tipo": `{"type":"otro"}`, "incompleta": `{"type":"bifrost_service_account"}`,
		"pem": `{"type":"bifrost_service_account","api":"a","account":"x","private_key_id":"k","private_key":"no-pem"}`} {
		if _, err := client.ParseKeyJSON([]byte(in)); err == nil {
			t.Errorf("%s: debe fallar", name)
		}
	}
}

func TestFederacionYPersona(t *testing.T) {
	s := startFake(t)
	ctx := context.Background()
	admin := newClient(t, s, client.StaticToken(fakeapi.DirectAdminTok), nil)
	if _, err := admin.CreateApp(ctx, client.CreateAppReq{Name: fakeapi.CIApp}); err != nil {
		t.Fatal(err)
	}
	ci := newClient(t, s, &client.IDTokenAuth{Grant: client.GrantFederated, App: fakeapi.CIApp, Read: func() (string, error) { return fakeapi.CIIDToken, nil }}, nil)
	if me, err := ci.Me(ctx); err != nil || me.IsAdmin {
		t.Fatalf("federado: %+v %v", me, err)
	}
	user := newClient(t, s, &client.IDTokenAuth{Grant: client.GrantUser, Read: func() (string, error) { return fakeapi.AdminIDToken, nil }}, nil)
	if me, err := user.Me(ctx); err != nil || !me.IsAdmin {
		t.Fatalf("persona: %+v %v", me, err)
	}
	bad := newClient(t, s, &client.IDTokenAuth{Grant: client.GrantFederated, Read: func() (string, error) { return "otro", nil }}, nil)
	if _, err := bad.Me(ctx); err == nil || !strings.Contains(err.Error(), "plane rechazó la credencial") {
		t.Fatalf("token malo: %v", err)
	}
	empty := newClient(t, s, &client.IDTokenAuth{Grant: client.GrantFederated, Read: func() (string, error) { return "", nil }}, nil)
	if _, err := empty.Me(ctx); err == nil || !strings.Contains(err.Error(), "vacío") {
		t.Fatalf("vacío: %v", err)
	}
}

func TestReintentosSoloEnLecturas(t *testing.T) {
	var gets, posts atomic.Int32
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/.well-known/bifrost", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"api": base + "/v1", "token_uri": base + "/auth/token"})
	})
	mux.HandleFunc("/v1/apps/x", func(w http.ResponseWriter, r *http.Request) {
		if gets.Add(1) < 3 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"no disponible"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"x"}`))
	})
	mux.HandleFunc("/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"error":"cluster caído"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	base = ts.URL
	c, _ := client.New(client.Config{API: ts.URL, Auth: client.StaticToken("t")})
	a, err := c.GetApp(context.Background(), "x")
	if err != nil || a.Name != "x" || gets.Load() != 3 {
		t.Fatalf("GET debe reintentar: %v gets=%d", err, gets.Load())
	}
	if _, err := c.CreateApp(context.Background(), client.CreateAppReq{Name: "y"}); err == nil || posts.Load() != 1 {
		t.Fatalf("POST no debe reintentarse: %v posts=%d", err, posts.Load())
	}
}

func TestTLSVerificaYAceptaCAPropia(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"api":"","token_uri":"x"}`))
	}))
	defer ts.Close()
	c, err := client.New(client.Config{API: ts.URL, Retries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Discover(context.Background()); err == nil {
		t.Fatal("sin la CA propia el TLS debe fallar: la verificación no se desactiva")
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	c2, err := client.New(client.Config{API: ts.URL, CAPEM: caPEM})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Discover(context.Background()); err != nil {
		t.Fatalf("con la CA debe funcionar: %v", err)
	}
	if _, err := client.New(client.Config{API: ts.URL, CAPEM: []byte("basura")}); err == nil {
		t.Fatal("una CA inválida debe rechazarse")
	}
}

func TestErrorDeRedSinSecretos(t *testing.T) {
	c, _ := client.New(client.Config{API: "http://127.0.0.1:1", Auth: client.StaticToken("secreto-123")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.GetApp(ctx, "x")
	if err == nil || strings.Contains(err.Error(), "secreto-123") {
		t.Fatalf("error de red: %v", err)
	}
}

func TestOperacionesTipadas(t *testing.T) {
	s := startFake(t)
	ctx := context.Background()
	c := newClient(t, s, client.StaticToken(fakeapi.DirectAdminTok), nil)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.CreateApp(ctx, client.CreateAppReq{Name: "tienda", Envs: []client.CreateEnvReq{{Name: "dev"}}})
	must(err)
	_, err = c.CreateEnv(ctx, "tienda", client.CreateEnvReq{Name: "prod", Quota: &client.Quota{CPU: "4", Memory: "8Gi", Pods: 40, PVCs: 10}})
	must(err)
	e, err := c.GetEnv(ctx, "tienda", "prod")
	must(err)
	if e.Quota.Pods != 40 || e.Namespace != "app-tienda-prod" {
		t.Fatalf("%+v", e)
	}
	_, err = c.SetNetwork(ctx, "tienda", "prod", client.NetworkReq{Domains: []string{"www.tienda.cl"}, InternetPorts: []int64{}, Egress: []client.Egress{}})
	must(err)
	_, err = c.AttachService(ctx, "tienda", "prod", client.AttachServiceReq{Service: "postgres", Options: map[string]string{"version": "16"}})
	must(err)
	conn, err := c.ServiceConnection(ctx, "tienda", "prod", "postgres")
	must(err)
	for _, k := range conn.Keys {
		if k.Secret && k.Value != "" {
			t.Fatalf("un valor secreto viajó: %+v", k)
		}
	}
	must(c.DetachService(ctx, "tienda", "prod", "postgres"))
	_, err = c.AddMember(ctx, "tienda", "ana@example.com", "deploy")
	must(err)
	_, err = c.SetMemberRole(ctx, "tienda", "ana@example.com", "readonly")
	must(err)
	must(c.RemoveMember(ctx, "tienda", "ana@example.com"))
	b, err := c.CreateBinding(ctx, "tienda", client.CreateBindingReq{Kind: "gitlab", ProjectPath: "acme/tienda", Role: "deploy", Claims: map[string]string{"environment": "production"}})
	must(err)
	must(c.RemoveBinding(ctx, "tienda", b.ID))
	_, err = c.CreateQueue(ctx, client.CreateQueueReq{Name: "mails", Kind: "bullmq"})
	must(err)
	qs, err := c.ListQueues(ctx)
	must(err)
	if len(qs) != 1 {
		t.Fatal(qs)
	}
	must(c.RemoveQueue(ctx, "bullmq", "mails"))
	_, err = c.AddAdmin(ctx, "otra@example.com")
	must(err)
	as, err := c.ListAdmins(ctx)
	must(err)
	if len(as) != 2 {
		t.Fatal(as)
	}
	must(c.RemoveAdmin(ctx, "otra@example.com"))
	if err := c.RemoveAdmin(ctx, fakeapi.BootstrapAdmin); err == nil || !strings.Contains(err.Error(), "arranque") {
		t.Fatalf("no se quita el de arranque: %v", err)
	}
	if err := c.PurgeApp(ctx, "tienda"); err == nil || !strings.Contains(err.Error(), "Archiva la App") {
		t.Fatalf("purgar sin archivar: %v", err)
	}
	_, err = c.ArchiveApp(ctx, "tienda")
	must(err)
	must(c.PurgeApp(ctx, "tienda"))
	if ex, _ := s.AppExists("tienda"); ex {
		t.Fatal("debió eliminarse")
	}
}
