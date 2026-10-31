// Command fakenis serves a canned apcupsd NIS response, so the exporter can run without a UPS.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mdlayher/apcupsd_exporter/internal/fakenis"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:3551", "TCP address to listen on")
	response := flag.String("response", "full", fmt.Sprintf("canned response to serve: %s", strings.Join(fakenis.Names(), ", ")))
	flag.Parse()

	messages, err := fakenis.Response(*response)
	if err != nil {
		log.Fatal(err)
	}

	srv, err := fakenis.Listen(*addr, messages)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()

	log.Printf("serving response %q on %s", *response, srv.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
