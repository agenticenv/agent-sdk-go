package restate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// startEndpoint starts the Restate SDK HTTP server. No-op when already started.
// Uses a TCP probe loop for readiness detection instead of a fixed sleep.
func (rt *RestateRuntime) startEndpoint() error {
	rt.endpoint.mu.Lock()
	defer rt.endpoint.mu.Unlock()

	if rt.endpoint.started {
		return nil
	}
	if rt.endpoint.server == nil {
		return fmt.Errorf("restate: endpoint server not configured")
	}

	addr := strings.TrimSpace(rt.endpoint.addr)
	if addr == "" {
		addr = defaultEndpointListenAddress
	}

	endpointCtx, endpointCancel := context.WithCancel(context.Background())
	rt.endpoint.cancel = endpointCancel

	startErr := make(chan error, 1)
	go func() {
		if err := rt.endpoint.server.Start(endpointCtx, addr); err != nil {
			startErr <- err
		}
	}()

	probeAddr := addr
	if strings.HasPrefix(probeAddr, ":") {
		probeAddr = "127.0.0.1" + probeAddr
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-startErr:
			endpointCancel()
			rt.endpoint.cancel = nil
			return fmt.Errorf("restate: endpoint failed on %s: %w", addr, err)
		default:
		}
		conn, dialErr := net.DialTimeout("tcp", probeAddr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			rt.endpoint.started = true
			rt.logger.Info(context.Background(), "restate endpoint ready",
				slog.String("scope", "runtime"),
				slog.String("listenAddress", addr))
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case err := <-startErr:
		endpointCancel()
		rt.endpoint.cancel = nil
		return fmt.Errorf("restate: endpoint failed on %s: %w", addr, err)
	default:
	}

	rt.endpoint.started = true
	rt.logger.Warn(context.Background(), "restate endpoint probe timed out; continuing",
		slog.String("scope", "runtime"),
		slog.String("listenAddress", addr))
	return nil
}

// registerDeployment POSTs this process's deployment URI to the Restate admin API when
// AdminURL is set. No-op when AdminURL is empty.
func (rt *RestateRuntime) registerDeployment(ctx context.Context) error {
	if rt.config == nil {
		return nil
	}

	admin := strings.TrimRight(strings.TrimSpace(rt.config.Endpoint.AdminURL), "/")
	if admin == "" {
		return nil
	}

	deployURL := strings.TrimSpace(rt.config.Endpoint.DeploymentURL)
	if deployURL == "" {
		deployURL = deploymentURLFromListen(rt.endpoint.addr)
	}

	body, err := json.Marshal(map[string]any{"uri": deployURL, "force": true})
	if err != nil {
		return err
	}

	client := rt.httpClient
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if ctx.Err() != nil {
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, admin+"/deployments", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if key := strings.TrimSpace(rt.config.Ingress.AuthKey); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("restate: register deployment %s via %s: %w", deployURL, admin, err)
		} else {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				rt.logger.Info(ctx, "restate deployment registered",
					slog.String("scope", "runtime"),
					slog.String("admin", admin),
					slog.String("deploymentURL", deployURL))
				return nil
			}
			msg := strings.TrimSpace(string(respBody))
			lastErr = fmt.Errorf("restate: register deployment %s via %s: status %d: %s",
				deployURL, admin, resp.StatusCode, msg)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
				resp.StatusCode != http.StatusConflict &&
				resp.StatusCode != http.StatusTooManyRequests {
				return lastErr
			}
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
		}
	}
	return lastErr
}

// deploymentURLFromListen derives the deployment callback URL from the SDK server listen address.
func deploymentURLFromListen(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		listen = defaultEndpointListenAddress
	}
	if strings.HasPrefix(listen, ":") {
		return "http://127.0.0.1" + listen
	}
	if strings.Contains(listen, "://") {
		return listen
	}
	return "http://" + listen
}
