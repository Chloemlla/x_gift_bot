package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"xgift/internal/site"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := site.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
