package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bltn/hl7gw/internal/console"
	"github.com/bltn/hl7gw/internal/gateway"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)

	err := gateway.Run(ctx, loadConfigPath(), openapiSpec, os.Stdout, console.Detect(os.Stdout))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: ", err)
		os.Exit(1)
	}
}

func loadConfigPath() string {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	_ = fs.Parse(os.Args[1:])

	return *configPath
}
