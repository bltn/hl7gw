package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bltn/hl7gw/internal/configure"
	"github.com/bltn/hl7gw/internal/console"
	"github.com/bltn/hl7gw/internal/gateway"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runGateway(os.Args[2:])
	case "configure":
		fs := flag.NewFlagSet("configure", flag.ExitOnError)
		output := fs.String("output", "hl7gw.json", "where to write the config file")
		_ = fs.Parse(os.Args[2:])
		err = configure.Run(*output, os.Stdin, os.Stdout, console.Detect(os.Stdout))
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: ", err)
		os.Exit(1)
	}
}

func runGateway(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return gateway.Run(ctx, *configPath, openapiSpec, os.Stdout, console.Detect(os.Stdout))
}

func usage() {
	fmt.Fprint(os.Stderr, "usage:\n  hl7gw run -config <path>\n  hl7gw configure [-output <path>]\n")
	os.Exit(2)
}
