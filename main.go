package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bltn/hl7gw/internal/config"
	"github.com/bltn/hl7gw/internal/configure"
	"github.com/bltn/hl7gw/internal/console"
	"github.com/bltn/hl7gw/internal/gateway"
	"github.com/bltn/hl7gw/internal/ops"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	args := os.Args[2:]
	term := console.Detect(os.Stdout)
	var err error
	switch os.Args[1] {
	case "run":
		err = runGateway(ctx, args, term)
	case "configure":
		fs := flag.NewFlagSet("configure", flag.ExitOnError)
		output := fs.String("output", config.DefaultPath, "where to write the config file")
		_ = fs.Parse(args)
		err = configure.Run(*output, os.Stdin, os.Stdout, term)
	case "messages":
		err = ops.Messages(ctx, args, os.Stdout, os.Stderr, term)
	case "replay":
		err = ops.Replay(ctx, args, os.Stdout, os.Stderr, term)
	case "process":
		err = ops.Process(ctx, args, os.Stdin, os.Stdout, os.Stderr, term)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: ", err)
		if _, ok := errors.AsType[*ops.UsageError](err); ok {
			usage()
		}
		os.Exit(1)
	}
}

func runGateway(ctx context.Context, args []string, term console.Terminal) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config file (default ./"+config.DefaultPath+")")
	_ = fs.Parse(args)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return gateway.Run(ctx, *configPath, os.Stdout, term)
}

func usage() {
	fmt.Fprint(os.Stderr, "usage:\n"+
		"  hl7gw run       [-config <path>]\n"+
		"  hl7gw configure [-output <path>]\n"+
		"  hl7gw messages  [-config <path>] [-pretty] [-limit <n>] [-before <id>] <channel> [<id>]\n"+
		"  hl7gw replay    [-config <path>] [-pretty] <channel> <id>\n"+
		"  hl7gw process   [-config <path>] [-pretty] [-dry-run] <channel> [<file>]\n")
	os.Exit(2)
}
