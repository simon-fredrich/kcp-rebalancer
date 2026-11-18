package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	opts, err := parse(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.apply || opts.timeout != 10*time.Minute || opts.rebalanceType != "simple" ||
		opts.kcp != "/tmp/kcp-admin.kubeconfig" || len(opts.shards) != 0 {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
}

func TestParseOverrides(t *testing.T) {
	opts, err := parse([]string{"--apply", "--migration-timeout=2m",
		"--kcp-kubeconfig=kcp.yaml", "--rebalance-type=simple",
		"--shard-server=root=https://localhost:8443/", "--shard-server=shard-1=https://localhost:8444"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.apply || opts.timeout != 2*time.Minute || opts.kcp != "kcp.yaml" ||
		opts.shards["root"] != "https://localhost:8443" || opts.shards["shard-1"] != "https://localhost:8444" {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestParseRejectsUnknownRebalanceType(t *testing.T) {
	_, err := parse([]string{"--rebalance-type=cpu-magic"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `unknown --rebalance-type "cpu-magic"; available: simple`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunRejectsArgumentsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{
		{"positional"}, {"--migration-timeout=0"}, {"--migration-timeout=-1m"},
		{"--migration-timeout=invalid"}, {"--unknown"}, {"--rebalance-type=unknown"}, {"--rebalance-type="},
		{"--hosting-kubeconfig=hosting.yaml"}, {"--namespace=kcp"},
		{"--shard-server=root=http://localhost:8443"}, {"--shard-server=root=https://localhost:8443/clusters/root"},
		{"--shard-server=https://localhost:8443"}, {"--shard-server==https://localhost:8443"},
		{"--shard-server=root=https://localhost:8443?query"}, {"--shard-server=root=https://user@localhost:8443"},
	} {
		if err := Run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestHelp(t *testing.T) {
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"--help"}, io.Discard, &stderr); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected help, got %v", err)
	}
	if !strings.Contains(stderr.String(), "Usage of rebalance:") || !strings.Contains(stderr.String(), "-apply") ||
		!strings.Contains(stderr.String(), "-rebalance-type") {
		t.Fatalf("unexpected help: %s", stderr.String())
	}
}
