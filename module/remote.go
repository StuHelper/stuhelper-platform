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
	"sync"
	"time"
)

var (
	ErrRemoteUnavailable   = errors.New("remote module unavailable")
	ErrRemoteConfiguration = errors.New("remote module configuration invalid")
	ErrRemoteIncompatible  = errors.New("remote module protocol incompatible")
)

// Remote's monitor owns network probes and credential reloads. Request handlers
// read its bounded snapshot; remote metadata can only confirm approved capabilities.
type Remote struct {
	Expected      Descriptor
	URL           string
	TokenFile     string
	Timeout       time.Duration
	ProbeInterval time.Duration
	MaxBackoff    time.Duration
}

func (config Remote) Definition() (Definition, error) {
	u, err := url.Parse(config.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Definition{}, ErrRemoteConfiguration
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return Definition{}, ErrRemoteConfiguration
		}
	}
	if config.Timeout <= 0 || config.Timeout > 10*time.Second {
		return Definition{}, ErrRemoteConfiguration
	}
	if config.ProbeInterval == 0 {
		config.ProbeInterval = 3 * time.Second
	}
	if config.MaxBackoff == 0 {
		config.MaxBackoff = 30 * time.Second
	}
	if config.ProbeInterval <= 0 || config.ProbeInterval > 30*time.Second || config.MaxBackoff < config.ProbeInterval || config.MaxBackoff > time.Minute {
		return Definition{}, ErrRemoteConfiguration
	}
	expected := config.Expected
	expected.Capabilities = slices.Clone(expected.Capabilities)
	u.Path = "/internal/v1/module"
	client := &http.Client{Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	probe := func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return ErrRemoteConfiguration
		}
		if config.TokenFile != "" {
			token, err := readServiceToken(config.TokenFile)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			return ErrRemoteUnavailable
		}
		defer response.Body.Close()
		if response.StatusCode >= 500 || response.StatusCode == 429 {
			return ErrRemoteUnavailable
		}
		if response.StatusCode == 401 || response.StatusCode == 403 {
			return ErrRemoteConfiguration
		}
		if response.StatusCode != http.StatusOK {
			return ErrRemoteIncompatible
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil {
			return ErrRemoteUnavailable
		}
		if len(body) > 65536 {
			return ErrRemoteIncompatible
		}
		var actual Descriptor
		if json.Unmarshal(body, &actual) != nil || actual.ID != expected.ID || actual.Version == "" || actual.API != expected.API {
			return ErrRemoteIncompatible
		}
		for _, name := range expected.Capabilities {
			if !slices.Contains(actual.Capabilities, name) {
				return ErrRemoteIncompatible
			}
		}
		return nil
	}
	var mu sync.RWMutex
	var checkedAt time.Time
	var lastError error = ErrRemoteUnavailable
	var cancel context.CancelFunc
	var done chan struct{}
	var active bool
	store := func(result error) {
		mu.Lock()
		checkedAt, lastError = time.Now(), result
		mu.Unlock()
	}
	start := func(ctx context.Context) error {
		mu.RLock()
		alreadyStarted := done != nil
		mu.RUnlock()
		if alreadyStarted {
			return ErrRemoteConfiguration
		}
		initialCtx, initialCancel := context.WithTimeout(ctx, min(config.Timeout, 200*time.Millisecond))
		result := probe(initialCtx)
		initialCancel()
		store(result)
		if ctx.Err() != nil {
			client.CloseIdleConnections()
			return ctx.Err()
		}
		if result != nil && !errors.Is(result, ErrRemoteUnavailable) {
			client.CloseIdleConnections()
			return result
		}
		monitorCtx, stop := context.WithCancel(context.Background())
		finished := make(chan struct{})
		mu.Lock()
		cancel, done, active = stop, finished, true
		mu.Unlock()
		go func() {
			defer close(finished)
			delay := config.ProbeInterval
			timer := time.NewTimer(delay)
			defer timer.Stop()
			for {
				select {
				case <-monitorCtx.Done():
					return
				case <-timer.C:
					result := probe(monitorCtx)
					store(result)
					if result == nil {
						delay = config.ProbeInterval
					} else {
						delay = min(delay*2, config.MaxBackoff)
					}
					timer.Reset(delay)
				}
			}
		}()
		return nil
	}
	ready := func(ctx context.Context) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mu.RLock()
		defer mu.RUnlock()
		if !active || checkedAt.IsZero() || time.Since(checkedAt) > 2*config.ProbeInterval+config.Timeout {
			return ErrRemoteUnavailable
		}
		return lastError
	}
	stop := func(ctx context.Context) error {
		mu.Lock()
		stop, finished := cancel, done
		lastError, active = ErrRemoteUnavailable, false
		mu.Unlock()
		if stop != nil {
			stop()
			select {
			case <-finished:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		client.CloseIdleConnections()
		return nil
	}
	return Definition{Descriptor: config.Expected, Start: start, Ready: ready, Stop: stop}, nil
}

func readServiceToken(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrRemoteConfiguration
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 {
		return "", ErrRemoteConfiguration
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", ErrRemoteConfiguration
	}
	return token, nil
}
