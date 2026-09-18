package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimePortPublication(t *testing.T) {
	for _, deployed := range []bool{false, true} {
		t.Run(map[bool]string{false: "private preview", true: "deployed"}[deployed], func(t *testing.T) {
			var created struct {
				HostConfig struct {
					PortBindings map[string][]struct{ HostIp, HostPort string }
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/containers/create":
					if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
						t.Error(err)
					}
					w.WriteHeader(http.StatusCreated)
				case "/containers/atoms-project-test/start":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			d := &dockerClient{client: &http.Client{Transport: rewriteDockerTransport{base: server.URL}}}
			err := d.createAndStart(context.Background(), Project{ID: "test", WorkspacePath: "/data/test/workspace", CodexStatePath: "/data/test/codex", Deployed: deployed}, "postgres://project", "runtime", "data", "network", "18000", 2, 1024, 256)
			if err != nil {
				t.Fatal(err)
			}
			bindings := created.HostConfig.PortBindings
			if !deployed {
				if len(bindings) != 0 {
					t.Fatalf("preview publishes host ports: %#v", bindings)
				}
			} else if b := bindings["3000/tcp"]; len(bindings) != 1 || len(b) != 1 || b[0].HostPort != "18000" || b[0].HostIp != "0.0.0.0" {
				t.Fatalf("deployment missing assigned port: %#v", bindings)
			}
		})
	}
}

type rewriteDockerTransport struct{ base string }

func (t rewriteDockerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	target, err := http.NewRequest(r.Method, t.base+r.URL.RequestURI(), nil)
	if err != nil {
		return nil, err
	}
	copy.URL = target.URL
	return http.DefaultTransport.RoundTrip(copy)
}

func TestRuntimePublicationMismatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deployed bool
		ports    []string
		want     bool
	}{
		{"private", false, nil, true},
		{"legacy preview exposed", false, []string{"18000"}, false},
		{"deployment", true, []string{"18000"}, true},
		{"deployment lost port", true, nil, false},
		{"wrong assigned port", true, []string{"18001"}, false},
		{"extra ports", true, []string{"18000", "18001"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimePublicationMatches(dockerContainer{PublishedPorts: tc.ports}, Project{DeployPort: 18000, Deployed: tc.deployed}); got != tc.want {
				t.Fatalf("publication matches = %v, want %v", got, tc.want)
			}
		})
	}
}
