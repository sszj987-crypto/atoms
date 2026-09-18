package platform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type dockerClient struct{ client *http.Client }
type dockerContainer struct {
	Exists         bool
	Running        bool
	PublishedPorts []string
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
		HostConfig struct {
			PortBindings map[string][]struct{ HostPort string } `json:"PortBindings"`
		} `json:"HostConfig"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return dockerContainer{}, err
	}
	container := dockerContainer{Exists: true, Running: raw.State.Running}
	for _, bindings := range raw.HostConfig.PortBindings {
		for _, binding := range bindings {
			container.PublishedPorts = append(container.PublishedPorts, binding.HostPort)
		}
	}
	return container, nil
}
func (d *dockerClient) ping(ctx context.Context) error {
	res, err := d.request(ctx, http.MethodGet, "/_ping", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return dockerError(res)
	}
	return nil
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
	bindings := map[string]any{}
	if p.Deployed {
		bindings["3000/tcp"] = []map[string]string{{"HostIp": "0.0.0.0", "HostPort": deployPort}}
	}
	body := map[string]any{
		"Image": image, "WorkingDir": "/workspace",
		"ExposedPorts": map[string]any{"3000/tcp": map[string]any{}},
		// The preview server is the Runtime's primary process. This avoids a
		// detached docker-exec race and lets Docker report a failed startup.
		"Cmd":    []string{"sh", "-lc", "if [ -f pnpm-lock.yaml ]; then pnpm install --frozen-lockfile --prefer-offline; else pnpm install --frozen-lockfile=false --prefer-offline; fi && pnpm dev --hostname 0.0.0.0 --port 3000"},
		"Env":    []string{"DATABASE_URL=" + databaseURL, "NEXT_TELEMETRY_DISABLED=1"},
		"Labels": map[string]string{"atoms.managed": "true", "atoms.project_id": p.ID},
		"HostConfig": map[string]any{
			"NanoCpus":     cpu * 1_000_000_000,
			"Memory":       memory,
			"PidsLimit":    pids,
			"NetworkMode":  network,
			"PortBindings": bindings,
			"Mounts": []map[string]any{
				{"Type": "volume", "Source": dataVolume, "Target": "/workspace", "VolumeOptions": map[string]any{"Subpath": volumeSubpath(p.WorkspacePath)}},
				{"Type": "volume", "Source": dataVolume, "Target": "/codex", "VolumeOptions": map[string]any{"Subpath": volumeSubpath(p.CodexStatePath)}},
			},
		},
	}
	log.Printf("docker createAndStart create project=%s deployed=%v portBindings=%v", p.ID, p.Deployed, bindings)
	res, err := d.request(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		createErr := dockerError(res)
		log.Printf("docker createAndStart create failed project=%s deployPort=%s err=%v", p.ID, deployPort, createErr)
		return createErr
	}
	res, err = d.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil)
	if err != nil {
		log.Printf("docker createAndStart start request failed project=%s deployPort=%s err=%v", p.ID, deployPort, err)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.remove(cleanupCtx, name)
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotModified {
		startErr := dockerError(res)
		log.Printf("docker createAndStart start failed project=%s deployPort=%s err=%v", p.ID, deployPort, startErr)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.remove(cleanupCtx, name)
		return startErr
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
	return d.execStream(ctx, name, cmd, env, nil)
}
func (d *dockerClient) execStream(ctx context.Context, name string, cmd, env []string, onLine func([]byte)) ([]byte, error) {
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
	var out bytes.Buffer
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 8<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := append(append([]byte(nil), scanner.Bytes()...), '\n')
		_, _ = out.Write(line)
		if onLine != nil {
			onLine(line)
		}
	}
	if err := scanner.Err(); err != nil {
		return out.Bytes(), err
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
		return out.Bytes(), fmt.Errorf("command exited %d", state.ExitCode)
	}
	return out.Bytes(), nil
}
func dockerError(res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return fmt.Errorf("docker API %s: %s", res.Status, strings.TrimSpace(string(raw)))
}
func runtimeName(projectID string) string         { return "atoms-project-" + projectID }
func runtimeWorkspacePath(project Project) string { return filepath.Clean(project.WorkspacePath) }

var errRuntimeUnavailable = errors.New("runtime unavailable")
