package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestGetConfig(t *testing.T) {
	t.Run("redacts non-empty sensitive final values", func(t *testing.T) {
		const (
			nonRealDefaultTokenSentinel = "NON_REAL_DEFAULT_TOKEN_SENTINEL"
			nonRealDefaultPassSentinel  = "NON_REAL_DEFAULT_PASSWORD_SENTINEL"
			nonRealCLITokenSentinel     = "NON_REAL_CLI_TOKEN_SENTINEL"
			nonRealCLIPassSentinel      = "NON_REAL_CLI_PASSWORD_SENTINEL"
		)

		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("discord-token", nonRealDefaultTokenSentinel, "")
		fs.String("mumble-password", nonRealDefaultPassSentinel, "")
		fs.String("discord-gid", "guild-123", "")
		fs.String("discord-cid", "channel-456", "")
		fs.Int("to-discord-buffer", 70, "")
		fs.Int("to-mumble-buffer", 80, "")
		fs.String("mode", "manual", "")
		if err := fs.Parse([]string{
			"-discord-token=" + nonRealCLITokenSentinel,
			"-mumble-password=" + nonRealCLIPassSentinel,
		}); err != nil {
			t.Fatalf("parse CLI overrides: %v", err)
		}

		got := getConfig(fs)
		want := []string{
			`discord-cid:"channel-456"`,
			`discord-gid:"guild-123"`,
			`discord-token:"[REDACTED]"`,
			`mode:"manual"`,
			`mumble-password:"[REDACTED]"`,
			`to-discord-buffer:"70"`,
			`to-mumble-buffer:"80"`,
		}
		if len(got) != 7 {
			t.Fatalf("getConfig returned %d items, want all 7 flags: %v", len(got), got)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getConfig() = %v, want %v", got, want)
		}

		output := strings.Join(got, "\n")
		for _, sentinel := range []string{
			nonRealDefaultTokenSentinel,
			nonRealDefaultPassSentinel,
			nonRealCLITokenSentinel,
			nonRealCLIPassSentinel,
		} {
			if strings.Contains(output, sentinel) {
				t.Errorf("getConfig output contains non-real secret sentinel %q", sentinel)
			}
		}
	})

	t.Run("preserves empty sensitive values", func(t *testing.T) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("discord-token", "", "")
		fs.String("mumble-password", "", "")

		got := getConfig(fs)
		want := []string{`discord-token:""`, `mumble-password:""`}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("getConfig() = %v, want %v", got, want)
		}
	})
}
