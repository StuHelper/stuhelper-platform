package module

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

var ErrRemoteUnavailable = errors.New("remote module unavailable")

// Remote uses a configured internal service endpoint. Remote responses cannot
// add new capabilities or routes to the locally reviewed composition.
type Remote struct {
	Expected  Descriptor
	URL       string
	TokenFile string
	Timeout   time.Duration
}

func (config Remote) Definition() (Definition, error) {
	u, err := url.Parse(config.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Definition{}, errors.New("invalid remote module origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return Definition{}, errors.New("remote modules require HTTPS except loopback IPs")
		}
	}
	if config.Timeout <= 0 || config.Timeout > 10*time.Second {
		return Definition{}, errors.New("remote timeout must be between zero and ten seconds")
	}
	u.Path = "/internal/v1/module"
	client := &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe := func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return ErrRemoteUnavailable
		}
		if config.TokenFile != "" {
			data, err := os.ReadFile(config.TokenFile)
			if err != nil || len(data) > 8192 {
				return ErrRemoteUnavailable
			}
			token := strings.TrimSpace(string(data))
			if token == "" || strings.ContainsAny(token, "\r\n") {
				return ErrRemoteUnavailable
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			return ErrRemoteUnavailable
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return ErrRemoteUnavailable
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || len(body) > 65536 {
			return ErrRemoteUnavailable
		}
		var actual Descriptor
		if json.Unmarshal(body, &actual) != nil {
			return ErrRemoteUnavailable
		}
		if actual.ID != config.Expected.ID || actual.Version == "" || actual.API != config.Expected.API {
			return ErrRemoteUnavailable
		}
		for _, capability := range config.Expected.Capabilities {
			if !slices.Contains(actual.Capabilities, capability) {
				return ErrRemoteUnavailable
			}
		}
		return nil
	}
	return Definition{Descriptor: config.Expected, Start: probe, Ready: probe, Stop: func(context.Context) error { client.CloseIdleConnections(); return nil }}, nil
}
