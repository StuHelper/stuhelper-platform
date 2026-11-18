package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/StuHelper/stuhelper-platform/account"
	"github.com/StuHelper/stuhelper-platform/admin"
	adminhttp "github.com/StuHelper/stuhelper-platform/admin/httpapi"
	"github.com/StuHelper/stuhelper-platform/application"
	e "github.com/StuHelper/stuhelper-platform/eligibility"
	elighttp "github.com/StuHelper/stuhelper-platform/eligibility/httpapi"
	sdk "github.com/StuHelper/stuhelper-platform/sdk"
	adminsdk "github.com/StuHelper/stuhelper-platform/sdk/admin"
	"github.com/StuHelper/stuhelper-platform/transport"
)

type fixedAuth struct{}

func (fixedAuth) Authenticate(r *http.Request) (transport.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer valid" {
		return transport.Principal{}, transport.ErrUnauthenticated
	}
	return account.Principal{Issuer: "urn:test", Subject: "admin-1"}, nil
}

type stubEligRepo struct {
	e.Defaults
}

func (stubEligRepo) Apply(_ context.Context, in e.Update) (e.Rule, error) {
	return e.Rule{Version: "2", Conditions: in.Change.Conditions}, nil
}

func TestUnifiedRoutesPropagateRequestIDAndDistinguishErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	routes := append(
		adminhttp.NewRoutes(admin.Service{}, fixedAuth{}),
		elighttp.NewRoutes(e.Service{Repo: stubEligRepo{}}, fixedAuth{}, func(_ context.Context, _ account.Principal, school string) error {
			if school != "buaa" {
				return e.ErrForbidden
			}
			return nil
		})...,
	)

	var unavailable atomic.Bool
	app, err := application.New(application.Config{Address: address, Enabled: []string{"platform"}}, application.Modules{
		PlatformReady: func(context.Context) error {
			if unavailable.Load() {
				return errors.New("fixture unavailable")
			}
			return nil
		},
		Routes:        routes,
		Authenticator: fixedAuth{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	})

	tr := &http.Transport{}
	client := &http.Client{Timeout: time.Second, Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	// 1. Admin endpoint unauthenticated error returns code + matching X-Request-ID.
	resp, err := client.Get("http://" + address + "/api/v1/platform/admin/operations")
	if err != nil {
		t.Fatal(err)
	}
	var adminErr adminsdk.AdminError
	if err = json.NewDecoder(resp.Body).Decode(&adminErr); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	headerID := resp.Header.Get("X-Request-ID")
	if resp.StatusCode != 401 || adminErr.Code != adminsdk.Unauthenticated || adminErr.RequestId == nil || *adminErr.RequestId != headerID || len(headerID) != 32 {
		t.Fatalf("unexpected admin error: status=%d headerID=%q body=%+v", resp.StatusCode, headerID, adminErr)
	}

	// 2. Eligibility PUT missing Idempotency-Key returns 400 invalid_request (distinct from invalid_rule).
	req, _ := http.NewRequest("PUT", "http://"+address+"/api/v1/platform/eligibility/buaa/content.enter", strings.NewReader(`{"expected_version":"1","conditions":[]}`))
	req.Header.Set("Authorization", "Bearer valid")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var eligErr sdk.Error
	if err = json.NewDecoder(resp.Body).Decode(&eligErr); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 400 || eligErr.Code != sdk.InvalidRequest || eligErr.RequestId == nil || *eligErr.RequestId != resp.Header.Get("X-Request-ID") {
		t.Fatalf("expected 400 invalid_request with request_id, got status=%d body=%+v", resp.StatusCode, eligErr)
	}

	// 3. Eligibility PUT with unknown action returns 400 invalid_rule with request_id.
	req, _ = http.NewRequest("PUT", "http://"+address+"/api/v1/platform/eligibility/buaa/unknown.action", strings.NewReader(`{"expected_version":"1","conditions":[]}`))
	req.Header.Set("Authorization", "Bearer valid")
	req.Header.Set("Idempotency-Key", "k1")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	eligErr = sdk.Error{}
	if err = json.NewDecoder(resp.Body).Decode(&eligErr); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 400 || eligErr.Code != sdk.InvalidRule || eligErr.RequestId == nil || *eligErr.RequestId != resp.Header.Get("X-Request-ID") {
		t.Fatalf("expected 400 invalid_rule with request_id, got status=%d body=%+v", resp.StatusCode, eligErr)
	}

	unavailable.Store(true)
	for _, path := range []string{"/api/v1/platform/admin/operations", "/api/v1/platform/eligibility/buaa/content.enter"} {
		resp, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusServiceUnavailable || body.Code != "unavailable" ||
			!sdk.ErrorCode(body.Code).Valid() || !adminsdk.AdminErrorCode(body.Code).Valid() ||
			body.RequestID == "" || body.RequestID != resp.Header.Get("X-Request-ID") {
			t.Fatalf("unready %s: status %d, body %+v, error %v", path, resp.StatusCode, body, err)
		}
	}

}

func TestSharedClientCasesMatchGeneratedContracts(t *testing.T) {
	raw, err := os.ReadFile("../contracts/client-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		AdminCases struct {
			Conflicts []struct {
				Status int                 `json:"status"`
				Body   adminsdk.AdminError `json:"body"`
			} `json:"conflicts"`
			OperationsPages []struct {
				Status int                     `json:"status"`
				Body   adminsdk.AdminAuditPage `json:"body"`
			} `json:"operations_pages"`
			IdempotentReplays []struct {
				Status int                    `json:"status"`
				Body   adminsdk.AdminRevision `json:"body"`
			} `json:"idempotent_replays"`
		} `json:"admin_cases"`
		EligibilityCases struct {
			Errors []struct {
				Status int       `json:"status"`
				Body   sdk.Error `json:"body"`
			} `json:"errors"`
			IdempotentReplays []struct {
				Status int      `json:"status"`
				Body   sdk.Rule `json:"body"`
			} `json:"idempotent_replays"`
		} `json:"eligibility_cases"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.AdminCases.Conflicts) == 0 || len(cases.AdminCases.OperationsPages) == 0 || len(cases.EligibilityCases.Errors) < 3 {
		t.Fatalf("shared client cases incomplete: %+v", cases)
	}
	for _, c := range cases.EligibilityCases.Errors {
		if !c.Body.Code.Valid() {
			t.Fatalf("invalid eligibility error enum in client-cases.json: %+v", c)
		}
	}
}
