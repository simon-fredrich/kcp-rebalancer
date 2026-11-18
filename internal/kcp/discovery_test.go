package kcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestConnect(t *testing.T) {
	const shards = `{"apiVersion":"core.kcp.io/v1alpha1","kind":"ShardList","items":[
		{"metadata":{"name":"shard-1"},"spec":{"baseURL":"https://shard-1.internal:6443"}},
		{"metadata":{"name":"root"},"spec":{"baseURL":"https://root.internal:6443/"}}
	]}`
	for _, tt := range []struct {
		name      string
		body      string
		status    int
		overrides map[string]string
		wantErr   string
	}{
		{name: "advertised endpoints", body: shards},
		{name: "forwarded endpoints", body: shards, overrides: map[string]string{
			"root": "https://localhost:8443", "shard-1": "https://localhost:8444/",
		}},
		{name: "unknown override", body: shards, overrides: map[string]string{"typo": "https://localhost:8444"}, wantErr: "unknown shard"},
		{name: "invalid override", body: shards, overrides: map[string]string{"root": "http://localhost:8443"}, wantErr: "expected HTTPS"},
		{name: "no shards", body: `{"apiVersion":"core.kcp.io/v1alpha1","kind":"ShardList","items":[]}`, wantErr: "no shards"},
		{name: "missing endpoint", body: `{"apiVersion":"core.kcp.io/v1alpha1","kind":"ShardList","items":[{"metadata":{"name":"root"}}]}`, wantErr: "expected HTTPS"},
		{name: "invalid endpoint type", body: `{"apiVersion":"core.kcp.io/v1alpha1","kind":"ShardList","items":[{"metadata":{"name":"root"},"spec":{"baseURL":42}}]}`, wantErr: "baseURL"},
		{name: "duplicate name", body: `{"apiVersion":"core.kcp.io/v1alpha1","kind":"ShardList","items":[{"metadata":{"name":"root"},"spec":{"baseURL":"https://root"}},{"metadata":{"name":"root"}}]}`, wantErr: "duplicate"},
		{name: "missing name", body: `{"apiVersion":"core.kcp.io/v1alpha1","kind":"ShardList","items":[{}]}`, wantErr: "missing or duplicate"},
		{name: "discovery failure", status: http.StatusForbidden, body: `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`, wantErr: "discover shards"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/clusters/root/apis/core.kcp.io/v1alpha1/shards" {
					t.Errorf("unexpected discovery path: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("Authorization") != "Bearer admin-token" {
					t.Error("missing KCP credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			config := clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{"kcp": {
					Server: server.URL + "/clusters/root", InsecureSkipTLSVerify: true,
				}},
				AuthInfos:      map[string]*clientcmdapi.AuthInfo{"admin": {Token: "admin-token"}},
				Contexts:       map[string]*clientcmdapi.Context{"admin": {Cluster: "kcp", AuthInfo: "admin"}},
				CurrentContext: "admin",
			}
			path := filepath.Join(t.TempDir(), "kubeconfig")
			if err := clientcmd.WriteToFile(config, path); err != nil {
				t.Fatal(err)
			}
			client, err := Connect(context.Background(), path, tt.overrides, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if client.rootConfig.Host != server.URL+"/clusters/root" {
				t.Fatalf("unexpected root endpoint: %s", client.rootConfig.Host)
			}
			if len(client.shards) != 2 || client.shards[0].Name != "root" || client.shards[1].Name != "shard-1" {
				t.Fatalf("unexpected shards: %+v", client.shards)
			}
			for _, shard := range client.shards {
				want := "https://" + shard.Name + ".internal:6443"
				if endpoint, ok := tt.overrides[shard.Name]; ok {
					want = strings.TrimRight(endpoint, "/")
				}
				if shard.Config.Host != want || shard.Config.BearerToken != "admin-token" ||
					!shard.Config.Insecure || shard.Config.ServerName != "" {
					t.Fatalf("incorrect endpoint or TLS credentials for %s", shard.Name)
				}
			}
		})
	}
}
