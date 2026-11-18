package kcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/simon-fredrich/kcp-rebalancer/internal/rebalance"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func TestMigrate(t *testing.T) {
	for _, phase := range []string{"Completed", "Failed", "Preparing"} {
		t.Run(phase, func(t *testing.T) {
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{migrationGVR: "LogicalClusterMigrationList"})
			client.PrependReactor("create", "logicalclustermigrations", func(action clienttesting.Action) (bool, runtime.Object, error) {
				obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
				if obj.Object["apiVersion"] != "migration.kcp.io/v1alpha1" || obj.GetGenerateName() != "rebalance-" {
					t.Fatalf("unexpected migration: %v", obj)
				}
				cluster, _, _ := unstructured.NestedString(obj.Object, "spec", "logicalCluster")
				destination, _, _ := unstructured.NestedString(obj.Object, "spec", "destinationShard")
				if cluster != "abc" || destination != "two" {
					t.Fatalf("wrong migration spec: %v", obj)
				}
				obj.SetName("rebalance-test")
				return true, obj, nil
			})
			client.PrependReactor("get", "logicalclustermigrations", func(action clienttesting.Action) (bool, runtime.Object, error) {
				if action.(clienttesting.GetAction).GetName() != "rebalance-test" {
					t.Fatal("polling the wrong migration")
				}
				return true, &unstructured.Unstructured{Object: map[string]interface{}{
					"status": map[string]interface{}{"phase": phase},
				}}, nil
			})
			err := migrate(context.Background(), client.Resource(migrationGVR),
				rebalance.Move{Workspace: rebalance.Workspace{Cluster: "abc", Path: "root:team"}, From: "one", To: "two"},
				10*time.Millisecond, io.Discard)
			if (err == nil) != (phase == "Completed") {
				t.Fatalf("phase %s: %v", phase, err)
			}
			if err != nil && !strings.Contains(err.Error(), "inspect before retrying") {
				t.Fatalf("error should explain recovery: %v", err)
			}
		})
	}
}

func TestCheckMigrations(t *testing.T) {
	for _, phase := range []string{"Completed", "Failed", "Preparing", ""} {
		t.Run("phase="+phase, func(t *testing.T) {
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{migrationGVR: "LogicalClusterMigrationList"})
			client.PrependReactor("list", "logicalclustermigrations", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, &unstructured.UnstructuredList{
					Items: []unstructured.Unstructured{{Object: map[string]interface{}{
						"metadata": map[string]interface{}{"name": "existing"},
						"status":   map[string]interface{}{"phase": phase},
					}}},
				}, nil
			})
			err := checkMigrations(context.Background(), client.Resource(migrationGVR))
			if (err == nil) != (phase == "Completed" || phase == "Failed") {
				t.Fatalf("phase %q: %v", phase, err)
			}
		})
	}
}

func TestMigrationAPIErrors(t *testing.T) {
	for _, verb := range []string{"list", "create", "get"} {
		t.Run(verb, func(t *testing.T) {
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{migrationGVR: "LogicalClusterMigrationList"})
			client.PrependReactor(verb, "logicalclustermigrations", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, fmt.Errorf("API unavailable")
			})
			resource := client.Resource(migrationGVR)
			var err error
			if verb == "list" {
				err = checkMigrations(context.Background(), resource)
			} else {
				client.PrependReactor("create", "logicalclustermigrations", func(action clienttesting.Action) (bool, runtime.Object, error) {
					if verb == "create" {
						return false, nil, nil
					}
					obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
					obj.SetName("test")
					return true, obj, nil
				})
				err = migrate(context.Background(), resource, rebalance.Move{}, time.Second, io.Discard)
			}
			if err == nil || !strings.Contains(err.Error(), "API unavailable") {
				t.Fatalf("API error not propagated: %v", err)
			}
			if verb == "get" && !strings.Contains(err.Error(), "inspect before retrying") {
				t.Fatalf("missing recovery guidance: %v", err)
			}
		})
	}
}

func TestMigrationClientUsesRootEndpoint(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			fmt.Fprint(w, `{"apiVersion":"migration.kcp.io/v1alpha1","kind":"LogicalClusterMigration","metadata":{"name":"rebalance-test"}}`)
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, "/rebalance-test") {
				fmt.Fprint(w, `{"apiVersion":"migration.kcp.io/v1alpha1","kind":"LogicalClusterMigration","metadata":{"name":"rebalance-test"},"status":{"phase":"Completed"}}`)
			} else {
				fmt.Fprint(w, `{"apiVersion":"migration.kcp.io/v1alpha1","kind":"LogicalClusterMigrationList","items":[]}`)
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	client := &Client{rootConfig: &rest.Config{Host: server.URL + "/clusters/root"}, out: io.Discard}
	if err := client.CheckMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Migrate(context.Background(), rebalance.Move{}, time.Second); err != nil {
		t.Fatal(err)
	}
	base := "/clusters/root/apis/migration.kcp.io/v1alpha1/logicalclustermigrations"
	want := "GET " + base + "\nPOST " + base + "\nGET " + base + "/rebalance-test"
	if got := strings.Join(requests, "\n"); got != want {
		t.Fatalf("unexpected requests:\n%s\nwant:\n%s", got, want)
	}
}
