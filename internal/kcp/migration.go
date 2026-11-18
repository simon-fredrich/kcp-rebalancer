package kcp

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/simon-fredrich/kcp-rebalancer/internal/rebalance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

var migrationGVR = schema.GroupVersionResource{
	Group: "migration.kcp.io", Version: "v1alpha1", Resource: "logicalclustermigrations",
}

func (c *Client) CheckMigrations(ctx context.Context) error {
	client, err := dynamic.NewForConfig(c.rootConfig)
	if err != nil {
		return err
	}
	return checkMigrations(ctx, client.Resource(migrationGVR))
}

func checkMigrations(ctx context.Context, resource dynamic.ResourceInterface) error {
	existing, err := resource.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list migrations in root (bind migration.kcp.io first): %w", err)
	}
	for _, migration := range existing.Items {
		phase, _, _ := unstructured.NestedString(migration.Object, "status", "phase")
		if phase != "Completed" && phase != "Failed" {
			return fmt.Errorf("migration %s is still active (phase %q); refusing to rebalance", migration.GetName(), phase)
		}
	}
	return nil
}

func (c *Client) Migrate(ctx context.Context, m rebalance.Move, timeout time.Duration) error {
	client, err := dynamic.NewForConfig(c.rootConfig)
	if err != nil {
		return err
	}
	return migrate(ctx, client.Resource(migrationGVR), m, timeout, c.out)
}

// A submitted migration can continue after migrate returns an error.
func migrate(ctx context.Context, resource dynamic.ResourceInterface, m rebalance.Move, timeout time.Duration, out io.Writer) error {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "migration.kcp.io/v1alpha1", "kind": "LogicalClusterMigration",
		"metadata": map[string]interface{}{"generateName": "rebalance-"},
		"spec":     map[string]interface{}{"logicalCluster": m.Workspace.Cluster, "destinationShard": m.To},
	}}
	created, err := resource.Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create migration for %s: %w", m.Workspace.Path, err)
	}
	fmt.Fprintf(out, "Created %s for %s\n", created.GetName(), m.Workspace.Path)
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		status, err := resource.Get(ctx, created.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(status.Object, "status", "phase")
		if phase == "Failed" {
			return false, fmt.Errorf("migration failed: %v", status.Object["status"])
		}
		return phase == "Completed", nil
	})
	if err != nil {
		return fmt.Errorf("waiting for %s: %w; migration may still be running, inspect before retrying", created.GetName(), err)
	}
	return nil
}
