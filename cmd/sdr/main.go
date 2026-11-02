package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jlambert/trackflights/internal/sdr"
)

func main() {
	addr := flag.String("listen", ":8090", "HTTP listen address")
	device := flag.String("device", "0", "RTL-SDR device index")
	gain := flag.String("gain", "auto", "receiver gain")
	ppm := flag.String("ppm", "0", "frequency correction")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := sdr.New()
	go func() {
		for ctx.Err() == nil {
			if err := s.RunReceiver(ctx, *device, *gain, *ppm); err != nil && ctx.Err() == nil {
				log.Printf("receiver: %v; retrying", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	server := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(c)
	}()
	log.Printf("SDR service listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
