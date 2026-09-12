package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type dockerClient struct{ client *http.Client }
type dockerContainer struct {
	Exists  bool
	Running bool
}

func newDockerClient() *dockerClient {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	// Long-running Codex invocations are bounded by their request context, not a
	// client-wide timeout. A 30-second HTTP timeout would abort valid agent runs.
	return &dockerClient{client: &http.Client{Transport: transport}}
}
func (d *dockerClient) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return d.client.Do(req)
}
func (d *dockerClient) inspect(ctx context.Context, name string) (dockerContainer, error) {
	res, err := d.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return dockerContainer{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return dockerContainer{}, nil
	}
	if res.StatusCode != http.StatusOK {
		return dockerContainer{}, dockerError(res)
	}
	var raw struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return dockerContainer{}, err
	}
	return dockerContainer{Exists: true, Running: raw.State.Running}, nil
}
func (d *dockerClient) managedProjectIDs(ctx context.Context) ([]string, error) {
	res, err := d.request(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(`{"label":["atoms.managed=true"]}`), nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, dockerError(res)
	}
	var rows []struct {
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		return nil, err
	}
	var ids []string
	for _, row := range rows {
		if id := row.Labels["atoms.project_id"]; id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (d *dockerClient) createAndStart(ctx context.Context, p Project, databaseURL, image, dataVolume, network, deployPort string, cpu, memory, pids int64) error {
	name := runtimeName(p.ID)
	body := map[string]any{
		"Image": image, "WorkingDir": "/workspace",
		// The preview server is the Runtime's primary process. This avoids a
		// detached docker-exec race and lets Docker report a failed startup.
		"Cmd":    []string{"sh", "-lc", "pnpm install --frozen-lockfile=false && pnpm dev --hostname 0.0.0.0 --port 3000"},
		"Env":    []string{"DATABASE_URL=" + databaseURL, "NEXT_TELEMETRY_DISABLED=1"},
		"Labels": map[string]string{"atoms.managed": "true", "atoms.project_id": p.ID},
		"HostConfig": map[string]any{
			"NanoCpus":    cpu * 1_000_000_000,
			"Memory":      memory,
			"PidsLimit":   pids,
			"NetworkMode": network,
			"PortBindings": map[string]any{"3000/tcp": []map[string]string{{"HostPort": deployPort}}},
			"Mounts": []map[string]any{
				{"Type": "volume", "Source": dataVolume, "Target": "/workspace", "VolumeOptions": map[string]any{"Subpath": volumeSubpath(p.WorkspacePath)}},
				{"Type": "volume", "Source": dataVolume, "Target": "/codex", "VolumeOptions": map[string]any{"Subpath": volumeSubpath(p.CodexStatePath)}},
			},
		},
	}
	res, err := d.request(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return dockerError(res)
	}
	res, err = d.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotModified {
		return dockerError(res)
	}
	return nil
}
func volumeSubpath(path string) string { return strings.TrimPrefix(filepath.Clean(path), "/data/") }
func (d *dockerClient) remove(ctx context.Context, name string) error {
	res, err := d.request(ctx, http.MethodDelete, "/containers/"+url.PathEscape(name)+"?force=true", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return dockerError(res)
}
func (d *dockerClient) exec(ctx context.Context, name string, cmd, env []string) ([]byte, error) {
	res, err := d.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/exec", map[string]any{"AttachStdout": true, "AttachStderr": true, "Tty": true, "Cmd": cmd, "Env": env, "WorkingDir": "/workspace", "User": "node"})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return nil, dockerError(res)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		return nil, err
	}
	res, err = d.request(ctx, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", map[string]any{"Detach": false, "Tty": true})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, dockerError(res)
	}
	out, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	res, err = d.request(ctx, http.MethodGet, "/exec/"+url.PathEscape(created.ID)+"/json", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var state struct {
		ExitCode int `json:"ExitCode"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&state) != nil {
		return nil, fmt.Errorf("docker exec inspect failed")
	}
	if state.ExitCode != 0 {
		return out, fmt.Errorf("command exited %d", state.ExitCode)
	}
	return out, nil
}
func dockerError(res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return fmt.Errorf("docker API %s: %s", res.Status, strings.TrimSpace(string(raw)))
}
func runtimeName(projectID string) string         { return "atoms-project-" + projectID }
func runtimeWorkspacePath(project Project) string { return filepath.Clean(project.WorkspacePath) }

var errRuntimeUnavailable = errors.New("runtime unavailable")
