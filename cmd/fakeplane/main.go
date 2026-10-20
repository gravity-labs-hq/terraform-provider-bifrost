// fakeplane levanta el servidor simulado de Bifröst (internal/fakeapi) para probar el provider con el terraform real, sin cluster.
// Imprime la URL en la primera línea y espera. NO es un servidor de verdad: solo para pruebas y demostraciones.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/fakeapi"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "dirección de escucha (solo loopback)")
	flag.Parse()
	s := fakeapi.New()
	url, err := s.Listen(*addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(url)
	fmt.Fprintf(os.Stderr, "fakeplane en %s (token de prueba: %s)\n", url, fakeapi.DirectAdminTok)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}
