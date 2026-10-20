package provider

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

func TestMain(m *testing.M) {
	pollInterval = 20 * time.Millisecond
	os.Exit(m.Run())
}

var testFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"bifrost": providerserver.NewProtocol6WithError(New("test")()),
}

// fakeEnv levanta el servidor simulado y entrega el bloque `provider` para las configuraciones de prueba.
type fakeEnv struct {
	srv *fakeapi.Server
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	s := fakeapi.New()
	s.Start()
	t.Cleanup(s.Close)
	return &fakeEnv{srv: s}
}

func (f *fakeEnv) provider() string {
	return fmt.Sprintf(`
provider "bifrost" {
  api   = %q
  token = %q
}
`, f.srv.URL, fakeapi.DirectAdminTok)
}

func (f *fakeEnv) cfg(body string) string { return f.provider() + body }
