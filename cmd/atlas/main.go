package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/cottrellashley/atlas/internal/adapters"
	"github.com/cottrellashley/atlas/internal/cli"
	"github.com/cottrellashley/atlas/internal/daemon"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, e := cli.New()
	if e == nil {
		if len(os.Args) > 1 && os.Args[1] == "__serve" {
			e = daemon.Serve(ctx, a.Home, adapters.New)
		} else {
			e = a.Run(ctx, os.Args[1:])
		}
	}
	if e != nil && !errors.Is(e, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "atlas:", e)
		os.Exit(1)
	}
}
