package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/simon-fredrich/kcp-rebalancer/internal/kcp"
	"github.com/simon-fredrich/kcp-rebalancer/internal/rebalance"
)

// planners lists the policies selectable with --rebalance-type.
var planners = map[string]func() rebalance.Planner{
	"simple": func() rebalance.Planner { return rebalance.SimplePlanner{} },
	"milp":   func() rebalance.Planner { return rebalance.MILPPlanner{} },
}

type shardOverrides map[string]string

func (s shardOverrides) String() string { return fmt.Sprint(map[string]string(s)) }
func (s shardOverrides) Set(value string) error {
	name, endpoint, ok := strings.Cut(value, "=")
	u, err := url.Parse(endpoint)
	if !ok || name == "" || err != nil || u.Scheme != "https" || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("expected shard-name=https://host:port, without a path")
	}
	s[name] = strings.TrimRight(endpoint, "/")
	return nil
}

type options struct {
	kcp           string
	rebalanceType string
	apply         bool
	timeout       time.Duration
	shards        shardOverrides
}

func parse(args []string, stderr io.Writer) (options, error) {
	opts := options{shards: shardOverrides{}}
	flags := flag.NewFlagSet("rebalance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kcp, "kcp-kubeconfig", "/tmp/kcp-admin.kubeconfig", "KCP system:masters kubeconfig")
	flags.StringVar(&opts.rebalanceType, "rebalance-type", "simple",
		"Balancing policy: "+strings.Join(plannerNames(), ", "))
	flags.Var(opts.shards, "shard-server", "repeatable shard-name=https://host:port override")
	flags.BoolVar(&opts.apply, "apply", false, "Execute migrations (default: print plan only)")
	flags.DurationVar(&opts.timeout, "migration-timeout", 10*time.Minute, "Maximum wait for each migration")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 || opts.timeout <= 0 {
		return opts, fmt.Errorf("unexpected positional arguments or non-positive migration timeout")
	}
	if _, exists := planners[opts.rebalanceType]; !exists {
		return opts, fmt.Errorf("unknown --rebalance-type %q; available: %s",
			opts.rebalanceType, strings.Join(plannerNames(), ", "))
	}
	return opts, nil
}

func plannerNames() []string {
	names := make([]string, 0, len(planners))
	for name := range planners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parse(args, stderr)
	if err != nil {
		return err
	}
	client, err := kcp.Connect(ctx, opts.kcp, opts.shards, stdout)
	if err != nil {
		return err
	}
	return rebalance.Run(ctx, client, stdout, rebalance.Options{
		Planner:          planners[opts.rebalanceType](),
		Apply:            opts.apply,
		MigrationTimeout: opts.timeout,
	})
}
