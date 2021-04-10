package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"

	"layeh.com/gumble/gumble"
)

type bridgeMode int

const (
	bridgeModeAuto bridgeMode = iota
	bridgeModeManual
	bridgeModeConstant
)

//BridgeConfig holds configuration information set at startup
//It should not change during runtime
type BridgeConfig struct {
	MumbleConfig               *gumble.Config
	MumbleAddr                 string
	MumbleInsecure             bool
	MumbleCertificate          string
	MumbleChannel              []string
	MumbleDisableText          bool
	Command                    string
	GID                        string
	CID                        string
	DiscordStartStreamingCount int
	DiscordDisableText         bool
}

func lookupEnvOrString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func lookupEnvOrInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		v, err := strconv.Atoi(val)
		if err != nil {
			log.Fatalf("LookupEnvOrInt[%s]: %v", key, err)
		}
		return v
	}
	return defaultVal
}

func lookupEnvOrBool(key string, defaultVal bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		v, err := strconv.ParseBool(val)
		if err != nil {
			log.Fatalf("LookupEnvOrInt[%s]: %v", key, err)
		}
		return v
	}
	return defaultVal
}

func getConfig(fs *flag.FlagSet) []string {
	cfg := make([]string, 0, 10)
	fs.VisitAll(func(f *flag.Flag) {
		cfg = append(cfg, fmt.Sprintf("%s:%q", f.Name, f.Value.String()))
	})

	return cfg
}
