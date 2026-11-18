package kcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/simon-fredrich/kcp-rebalancer/internal/rebalance"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

// Inventory reads each shard sequentially; this is not an atomic snapshot.
func (c *Client) Inventory(ctx context.Context) (rebalance.Inventory, error) {
	result := make(rebalance.Inventory)
	seen := map[string]bool{}
	for _, shard := range c.shards {
		client, err := rest.HTTPClientFor(shard.Config)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			shard.Config.Host+"/clusters/*/apis/core.kcp.io/v1alpha1/logicalclusters", nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("inventory %s: %w", shard.Name, err)
		}
		list, err := readClusters(resp)
		if err != nil {
			return nil, fmt.Errorf("inventory %s: %w", shard.Name, err)
		}
		result[shard.Name] = []rebalance.Workspace{}
		for _, obj := range list.Items {
			a := obj.GetAnnotations()
			cluster := a["kcp.io/cluster"]
			if cluster == "root" || strings.HasPrefix(cluster, "system:") {
				continue
			}
			if cluster == "" {
				return nil, fmt.Errorf("logical cluster on %s has missing kcp.io/cluster annotation", shard.Name)
			}
			path := a["kcp.io/path"]
			if path == "" {
				return nil, fmt.Errorf("cluster %q on %s has missing workspace path (kcp.io/path annotation)", cluster, shard.Name)
			}
			phase, _, err := unstructured.NestedString(obj.Object, "status", "phase")
			if err != nil {
				return nil, fmt.Errorf("cluster %q on %s: %w", cluster, shard.Name, err)
			}
			if phase != "Ready" {
				return nil, fmt.Errorf("cluster %q on %s has non-Ready phase %q", cluster, shard.Name, phase)
			}
			if seen[cluster] {
				return nil, fmt.Errorf("cluster %s appears on multiple shards; migration may be active", cluster)
			}
			seen[cluster] = true
			result[shard.Name] = append(result[shard.Name], rebalance.Workspace{Cluster: cluster, Path: path, Weight: 1})
		}
	}
	return result, nil
}

func readClusters(resp *http.Response) (*unstructured.UnstructuredList, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, body)
	}
	var list unstructured.UnstructuredList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	if list.GetKind() != "LogicalClusterList" {
		return nil, fmt.Errorf("expected LogicalClusterList, got %q", list.GetKind())
	}
	return &list, nil
}
