// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ethereum/go-ethereum/log"
	koanfjson "github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/offchainlabs/nitro/cmd/conf"
	"github.com/offchainlabs/nitro/cmd/util"
	"github.com/offchainlabs/nitro/das"
	"github.com/offchainlabs/nitro/das/dasrpc"
	"github.com/pkg/errors"
	flag "github.com/spf13/pflag"
)

type DAServerConfig struct {
	Port       uint64                     `koanf:"port"`
	LogLevel   int                        `koanf:"log-level"`
	DAConf     das.DataAvailabilityConfig `koanf:"data-availability"`
	ConfConfig conf.ConfConfig            `koanf:"conf"`
}

func main() {
	if err := startup(); err != nil {
		log.Error("Error running DAServer", "err", err)
	}
}

func printSampleUsage() {
	progname := os.Args[0]
	fmt.Printf("\n")
	fmt.Printf("Sample usage:                  %s --help \n", progname)
}

func parseDAServer(args []string) (*DAServerConfig, error) {
	f := flag.NewFlagSet("daserver", flag.ContinueOnError)

	f.Int("log-level", int(log.LvlInfo), "log level")
	f.Uint64("port", 9876, "Port to listen on")
	das.DataAvailabilityConfigAddOptions("data-availability", f)
	conf.ConfConfigAddOptions("conf", f)

	k, err := util.BeginCommonParse(f, args)
	if err != nil {
		return nil, err
	}

	var serverConfig DAServerConfig
	if err := util.EndCommonParse(k, &serverConfig); err != nil {
		return nil, err
	}
	if serverConfig.ConfConfig.Dump {
		// Print out current configuration

		// Don't keep printing configuration file
		err := k.Load(confmap.Provider(map[string]interface{}{
			"conf.dump": false,
		}, "."), nil)
		if err != nil {
			return nil, errors.Wrap(err, "error removing extra parameters before dump")
		}

		c, err := k.Marshal(koanfjson.Parser())
		if err != nil {
			return nil, errors.Wrap(err, "unable to marshal config file to JSON")
		}

		fmt.Println(string(c))
		os.Exit(0)
	}

	return &serverConfig, nil
}

func startup() error {
	vcsRevision, vcsTime := conf.GetVersion()
	serverConfig, err := parseDAServer(os.Args[1:])
	if err != nil {
		fmt.Printf("\nrevision: %v, vcs.time: %v\n", vcsRevision, vcsTime)
		printSampleUsage()
		if !strings.Contains(err.Error(), "help requested") {
			fmt.Printf("%s\n", err.Error())
		}
		return nil
	}

	glogger := log.NewGlogHandler(log.StreamHandler(os.Stderr, log.TerminalFormat(false)))
	glogger.Verbosity(log.Lvl(serverConfig.LogLevel))
	log.Root().SetHandler(glogger)

	log.Info("Starting daserver", "port", serverConfig.Port)

	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mode, err := serverConfig.DAConf.Mode()
	if err != nil {
		return err
	}
	var dasImpl das.DataAvailabilityService
	switch mode {
	case das.LocalDataAvailability:
		dasImpl, err = das.NewLocalDiskDAS(serverConfig.DAConf.LocalDiskDASConfig)
		if err != nil {
			return err
		}
	case das.AggregatorDataAvailability:
		dasImpl, err = dasrpc.NewRPCAggregator(serverConfig.DAConf.AggregatorConfig)
		if err != nil {
			return err
		}
	default:
		panic("Only local DAS implementation supported for daserver currently.")
	}

	server, err := dasrpc.StartDASRPCServer(ctx, serverConfig.Port, dasImpl)
	if err != nil {
		return err
	}
	<-sigint
	server.Stop()

	return nil
}
