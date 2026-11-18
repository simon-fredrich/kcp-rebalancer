package kcp

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type shard struct {
	Name   string
	Config *rest.Config
}

type Client struct {
	shards     []shard
	rootConfig *rest.Config
	out        io.Writer
}

// Connect discovers shards through root and uses externally reachable endpoints.
func Connect(ctx context.Context, kubeconfig string, overrides map[string]string, stdout io.Writer) (*Client, error) {
	kcpConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	kcpConfig.Timeout = 30 * time.Second
	base, _, _ := strings.Cut(kcpConfig.Host, "/clusters/")
	kcpConfig.Host = strings.TrimRight(base, "/") + "/clusters/root"
	rootClient, err := dynamic.NewForConfig(kcpConfig)
	if err != nil {
		return nil, err
	}
	shards, err := rootClient.Resource(schema.GroupVersionResource{
		Group: "core.kcp.io", Version: "v1alpha1", Resource: "shards",
	}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("discover shards: %w", err)
	}
	client := &Client{rootConfig: kcpConfig, out: stdout}
	names := map[string]bool{}
	for _, obj := range shards.Items {
		name := obj.GetName()
		if name == "" || names[name] {
			return nil, fmt.Errorf("missing or duplicate shard name %q", name)
		}
		names[name] = true
		endpoint, overridden := overrides[name]
		if !overridden {
			endpoint, _, err = unstructured.NestedString(obj.Object, "spec", "baseURL")
			if err != nil {
				return nil, fmt.Errorf("shard %s baseURL: %w", name, err)
			}
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("shard %s: expected HTTPS endpoint without a path, got %q", name, endpoint)
		}
		config := rest.CopyConfig(kcpConfig)
		config.Host = strings.TrimRight(endpoint, "/")
		client.shards = append(client.shards, shard{Name: name, Config: config})
	}
	if len(client.shards) == 0 {
		return nil, fmt.Errorf("no shards discovered")
	}
	for name := range overrides {
		if !names[name] {
			return nil, fmt.Errorf("endpoint override for unknown shard %q", name)
		}
	}
	sort.Slice(client.shards, func(i, j int) bool { return client.shards[i].Name < client.shards[j].Name })
	return client, nil
}
