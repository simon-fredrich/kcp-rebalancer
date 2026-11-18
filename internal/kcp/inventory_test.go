package kcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestInventory(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		wantErr    bool
	}{
		{"valid", `{"kind":"LogicalClusterList","items":[
			{"metadata":{"annotations":{"kcp.io/cluster":"root"}}},
			{"metadata":{"annotations":{"kcp.io/cluster":"system:shard"}}},
			{"metadata":{"annotations":{"kcp.io/cluster":"abc","kcp.io/path":"root:team"}},"status":{"phase":"Ready"}}]}`, 200, false},
		{"empty", `{"kind":"LogicalClusterList","items":[]}`, 200, false},
		{"forbidden", `denied`, 403, true},
		{"wrong-kind", `{"kind":"WorkspaceList","items":[]}`, 200, true},
		{"invalid-json", `{`, 200, true},
		{"not-ready", `{"kind":"LogicalClusterList","items":[{"metadata":{"annotations":{"kcp.io/cluster":"abc","kcp.io/path":"root:team"}},"status":{"phase":"Scheduling"}}]}`, 200, true},
		{"missing-path", `{"kind":"LogicalClusterList","items":[{"metadata":{"annotations":{"kcp.io/cluster":"abc"}},"status":{"phase":"Ready"}}]}`, 200, true},
		{"home", `{"kind":"LogicalClusterList","items":[{"metadata":{"annotations":{"kcp.io/cluster":"abc","kcp.io/path":"user:kcp-admin"}},"status":{"phase":"Ready"}}]}`, 200, false},
		{"home-child", `{"kind":"LogicalClusterList","items":[{"metadata":{"annotations":{"kcp.io/cluster":"abc","kcp.io/path":"user:kcp-admin:org-1"}},"status":{"phase":"Ready"}}]}`, 200, false},
		{"missing-cluster", `{"kind":"LogicalClusterList","items":[{"metadata":{"annotations":{"kcp.io/path":"root:team"}},"status":{"phase":"Ready"}}]}`, 200, true},
		{"invalid-phase", `{"kind":"LogicalClusterList","items":[{"metadata":{"annotations":{"kcp.io/cluster":"abc","kcp.io/path":"root:team"}},"status":{"phase":42}}]}`, 200, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/clusters/*/apis/core.kcp.io/v1alpha1/logicalclusters" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client := &Client{shards: []shard{{Name: "one", Config: &rest.Config{Host: server.URL}}}}
			result, err := client.Inventory(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			switch tt.name {
			case "home", "home-child":
				wantPath := "user:kcp-admin"
				if tt.name == "home-child" {
					wantPath += ":org-1"
				}
				items := result["one"]
				if len(items) != 1 || items[0].Cluster != "abc" || items[0].Path != wantPath || items[0].Weight != 1 {
					t.Fatalf("expected workspace with path %q, got %v", wantPath, items)
				}
			case "missing-path":
				if !strings.Contains(err.Error(), "missing workspace path") || strings.Contains(err.Error(), "non-Ready") {
					t.Fatalf("expected path-specific error, got %v", err)
				}
			case "not-ready":
				if !strings.Contains(err.Error(), `non-Ready phase "Scheduling"`) || strings.Contains(err.Error(), "missing workspace path") {
					t.Fatalf("expected phase-specific error, got %v", err)
				}
			case "missing-cluster":
				if !strings.Contains(err.Error(), "missing kcp.io/cluster annotation") {
					t.Fatalf("expected cluster-specific error, got %v", err)
				}
			}
			if tt.name == "empty" {
				if items, exists := result["one"]; !exists || len(items) != 0 {
					t.Fatalf("empty shard must remain a destination: %v", result)
				}
			}
			if tt.name == "valid" {
				if len(result["one"]) != 1 {
					t.Fatalf("expected one non-system workspace: %v", result)
				}
				client.shards = append(client.shards, shard{Name: "two", Config: &rest.Config{Host: server.URL}})
				_, err := client.Inventory(context.Background())
				if err == nil || !strings.Contains(err.Error(), "multiple shards") {
					t.Fatalf("expected duplicate placement error, got %v", err)
				}
			}
		})
	}
}
