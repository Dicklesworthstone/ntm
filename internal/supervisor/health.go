package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
)

const maxDaemonHealthResponse = 1 << 20

func daemonHealthMode(spec DaemonSpec) string {
	switch {
	case spec.HealthMCP:
		return "mcp"
	case spec.HealthURL != "":
		return "http"
	case len(spec.HealthCmd) > 0:
		return "command"
	default:
		return "process"
	}
}

// probeDaemonHealth uses the same execution scope as the daemon. The caller
// supplies the generation's bounded health context, not its process context:
// stopping observation must not skip graceful shutdown of the daemon itself.
func probeDaemonHealth(ctx context.Context, spec DaemonSpec, projectDir string) error {
	dir := spec.WorkDir
	if dir == "" {
		dir = projectDir
	}
	return probeDaemonHealthInScope(ctx, spec, dir, append(os.Environ(), spec.Env...))
}

func probeDaemonHealthInScope(ctx context.Context, spec DaemonSpec, dir string, env []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch daemonHealthMode(spec) {
	case "mcp":
		return probeDaemonMCP(ctx, spec.HealthURL)
	case "http":
		_, err := daemonHealthResponse(ctx, http.MethodGet, spec.HealthURL, nil, false)
		return err
	case "command":
		cmd := exec.CommandContext(ctx, spec.HealthCmd[0], spec.HealthCmd[1:]...)
		cmd.WaitDelay = daemonHealthWaitDelay
		cmd.Dir = dir
		cmd.Env = append([]string(nil), env...)
		setSysProcAttr(cmd)
		cmd.Cancel = func() error {
			// The probe owns this newly created process group, not a recovered
			// PID. Do not leave health-command descendants running on cancel.
			forceKillProcess(cmd.Process)
			return nil
		}
		if err := cmd.Run(); err != nil {
			return errors.Join(ctx.Err(), fmt.Errorf("daemon health command: %w", err))
		}
	}
	return ctx.Err()
}

func daemonHealthResponse(ctx context.Context, method, url string, body []byte, requireOK bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := healthCheckClient.Do(req)
	if err != nil {
		return nil, errors.Join(ctx.Err(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (requireOK && resp.StatusCode != http.StatusOK) {
		return nil, fmt.Errorf("daemon health HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDaemonHealthResponse+1))
	if err != nil {
		return nil, errors.Join(ctx.Err(), fmt.Errorf("read daemon health response: %w", err))
	}
	if len(data) > maxDaemonHealthResponse {
		return nil, errors.New("daemon health response exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func probeDaemonMCP(ctx context.Context, url string) error {
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ntm-supervisor","version":"health-probe"}}}`)
	data, err := daemonHealthResponse(ctx, http.MethodPost, url, request, true)
	if err != nil {
		return err
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	// Unmarshal consumes the entire bounded response: a valid JSON prefix
	// followed by truncation, hidden data, or another object is not healthy.
	if err := json.Unmarshal(data, &envelope); err != nil {
		return errors.New("daemon health response is not a complete JSON-RPC object")
	}
	if envelope.JSONRPC != "2.0" || !bytes.Equal(bytes.TrimSpace(envelope.ID), []byte("1")) {
		return errors.New("daemon health response does not match the initialize request")
	}
	if len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null")) {
		return errors.New("daemon health initialize returned a JSON-RPC error")
	}
	var result struct {
		ServerInfo *struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil || result.ServerInfo == nil || result.ServerInfo.Name == "" {
		return errors.New("daemon health response has no initialize server identity")
	}
	return ctx.Err()
}
