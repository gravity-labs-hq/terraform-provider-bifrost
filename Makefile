# Provider de Terraform para Bifröst.
#   make build     compila bin/terraform-provider-bifrost
#   make test      go vet + go test -race (usa el servidor simulado internal/fakeapi y el terraform del PATH; sin red ni cluster)
#   make smoke     compila y ejecuta el terraform real contra el servidor simulado (examples/tienda)
#   make install   deja el binario para dev_overrides en ~/.terraform.d/plugins/local/gravity-labs-hq/bifrost (ver README)
#   make docs      regenera docs/ con tfplugindocs (go run; necesita terraform en el PATH)
#   make release-snapshot   goreleaser en modo prueba (sin publicar)
VERSION ?= dev
BIN := bin/terraform-provider-bifrost
OS_ARCH := $(shell go env GOOS)_$(shell go env GOARCH)

.PHONY: build test vet smoke install docs fmt release-snapshot
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) .
vet:
	go vet ./...
test: vet
	go test -race -count=1 ./...
smoke:
	./scripts/smoke.sh
install: build
	mkdir -p $(HOME)/.terraform.d/plugins/local/gravity-labs-hq/bifrost
	cp $(BIN) $(HOME)/.terraform.d/plugins/local/gravity-labs-hq/bifrost/terraform-provider-bifrost
	@echo "Para usarlo: ver README (dev_overrides apuntando a $(HOME)/.terraform.d/plugins/local/gravity-labs-hq/bifrost)"
docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.21.0 generate --provider-name bifrost --rendered-provider-name "Bifröst"
fmt:
	gofmt -w .
release-snapshot:
	goreleaser release --snapshot --clean
