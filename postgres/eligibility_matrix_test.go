package postgres_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/eligibility/httpapi"
	sdk "github.com/StuHelper/stuhelper-platform/sdk"
)

func TestEligibilitySchoolMatrixOverrideResetAndPerSchoolRevisions(t *testing.T) {
	repo, owner := setup(t)
	cached := &e.CachedRules{Repo: repo}
	svc := e.Service{Repo: cached}

	mux := http.NewServeMux()
	httpapi.Handler{
		Service:      svc,
		Authenticate: func(*http.Request) (string, error) { return "admin:buaa", nil },
		Authorize:    func(context.Context, string, string) error { return nil },
	}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial whole-school matrix returns all 15 actions with initial_default source.
	matrix, err := client.GetSchoolEligibilityWithResponse(ctx, "buaa")
	if err != nil || matrix.JSON200 == nil || matrix.JSON200.School != "buaa" || matrix.JSON200.Version != "1" || len(matrix.JSON200.Rules) != 15 {
		t.Fatalf("unexpected initial school matrix: %+v, err=%v", matrix, err)
	}
	uploadInitial := matrix.JSON200.Rules["resources.upload"]
	if uploadInitial.Source == nil || *uploadInitial.Source != sdk.InitialDefault {
		t.Fatalf("expected initial_default source, got %+v", uploadInitial)
	}
	if uploadInitial.SupportedConditions == nil || len(*uploadInitial.SupportedConditions) != 4 {
		t.Fatalf("unexpected supported_conditions for resources.upload: %+v", uploadInitial.SupportedConditions)
	}
	courseInitial := matrix.JSON200.Rules["publish_course"]
	if courseInitial.SupportedConditions == nil || len(*courseInitial.SupportedConditions) != 4 {
		t.Fatalf("unexpected supported_conditions for publish_course: %+v", courseInitial.SupportedConditions)
	}

	// Unsupported condition course_enrollment on non-course action is rejected with 400.
	badReason := "invalid enrollment condition"
	badPut, err := client.SetEligibilityRuleWithResponse(ctx, "buaa", sdk.ResourcesUpload, &sdk.SetEligibilityRuleParams{IdempotencyKey: "bad-cond"}, sdk.Change{
		ExpectedVersion: "1",
		Conditions:      []sdk.ChangeConditions{sdk.ChangeConditions("course_enrollment")},
		Reason:          &badReason,
	})
	if err != nil || badPut.StatusCode() != 400 {
		t.Fatalf("expected 400 for unsupported condition, got %+v, err=%v", badPut, err)
	}

	// 2. Global "*" update changes inherited schools to source="global" without bumping school-specific revision.
	globalReason := "tighten global upload with real-name verification"
	globalPut, err := client.SetEligibilityRuleWithResponse(ctx, "*", sdk.ResourcesUpload, &sdk.SetEligibilityRuleParams{IdempotencyKey: "global-upload-1"}, sdk.Change{
		ExpectedVersion: "1",
		Conditions:      []sdk.ChangeConditions{sdk.ChangeConditionsCampusEligibility, sdk.ChangeConditionsPhoneBound, sdk.ChangeConditionsIdentityVerified},
		Reason:          &globalReason,
	})
	if err != nil || globalPut.JSON200 == nil || globalPut.JSON200.Version != "2" {
		t.Fatalf("global PUT failed: %+v, err=%v", globalPut, err)
	}

	inherited, err := client.GetEligibilityRuleWithResponse(ctx, "buaa", sdk.ResourcesUpload)
	if err != nil || inherited.JSON200 == nil || *inherited.JSON200.Source != sdk.Global || len(inherited.JSON200.Conditions) != 3 {
		t.Fatalf("buaa did not inherit global update: %+v, err=%v", inherited, err)
	}

	// 3. Per-school revision isolation: editing buaa does not cause 409 conflict for tsinghua.
	tsinghuaBefore, err := client.GetSchoolEligibilityWithResponse(ctx, "tsinghua")
	if err != nil || tsinghuaBefore.JSON200 == nil || tsinghuaBefore.JSON200.Version != "1" {
		t.Fatalf("unexpected tsinghua initial matrix: %+v, err=%v", tsinghuaBefore, err)
	}

	buaaReason := "buaa override relaxes identity_verified temporarily"
	buaaPut, err := client.SetEligibilityRuleWithResponse(ctx, "buaa", sdk.ResourcesUpload, &sdk.SetEligibilityRuleParams{IdempotencyKey: "buaa-upload-1"}, sdk.Change{
		ExpectedVersion: inherited.JSON200.Version,
		Conditions:      []sdk.ChangeConditions{sdk.ChangeConditionsCampusEligibility},
		Reason:          &buaaReason,
	})
	if err != nil || buaaPut.JSON200 == nil || buaaPut.JSON200.Version != "2" || *buaaPut.JSON200.Source != sdk.SchoolOverride {
		t.Fatalf("buaa override failed: %+v, err=%v", buaaPut, err)
	}
	// global_conditions remains 3 items so buaa administrator can compare differences.
	if buaaPut.JSON200.GlobalConditions == nil || len(*buaaPut.JSON200.GlobalConditions) != 3 {
		t.Fatalf("expected 3 global_conditions on override response, got %+v", buaaPut.JSON200.GlobalConditions)
	}

	// Tsinghua administrator saves using version "1" read earlier: succeeds without cross-school 409!
	tsinghuaReason := "tsinghua independent purchase rule"
	tsinghuaPut, err := client.SetEligibilityRuleWithResponse(ctx, "tsinghua", sdk.ResourcesPurchase, &sdk.SetEligibilityRuleParams{IdempotencyKey: "tsinghua-purchase-1"}, sdk.Change{
		ExpectedVersion: tsinghuaBefore.JSON200.Version,
		Conditions:      []sdk.ChangeConditions{sdk.ChangeConditionsCampusEligibility, sdk.ChangeConditionsPhoneBound},
		Reason:          &tsinghuaReason,
	})
	if err != nil || tsinghuaPut.JSON200 == nil || tsinghuaPut.JSON200.Version != "2" {
		t.Fatalf("cross-school edit caused conflict: %+v, err=%v", tsinghuaPut, err)
	}

	// 4. DELETE school override restores inheritance from global "*" and records audit reason.
	forbidGlobalDel, err := client.ResetEligibilityRuleWithResponse(ctx, "*", sdk.ResourcesUpload, &sdk.ResetEligibilityRuleParams{IdempotencyKey: "del-star"}, sdk.Reset{
		ExpectedVersion: "2",
		Reason:          "cannot delete global",
	})
	if err != nil || forbidGlobalDel.StatusCode() != 400 {
		t.Fatalf("expected 400 deleting global *, got %+v, err=%v", forbidGlobalDel, err)
	}

	// Deleting an action with no school override returns 409.
	noOverrideDel, err := client.ResetEligibilityRuleWithResponse(ctx, "buaa", sdk.ReplyTeacher, &sdk.ResetEligibilityRuleParams{IdempotencyKey: "del-absent"}, sdk.Reset{
		ExpectedVersion: "2",
		Reason:          "no override present",
	})
	if err != nil || noOverrideDel.StatusCode() != 409 {
		t.Fatalf("expected 409 deleting non-overridden action, got %+v, err=%v", noOverrideDel, err)
	}

	resetReason := "restore buaa upload rule to global baseline"
	resetResp, err := client.ResetEligibilityRuleWithResponse(ctx, "buaa", sdk.ResourcesUpload, &sdk.ResetEligibilityRuleParams{IdempotencyKey: "buaa-reset-1"}, sdk.Reset{
		ExpectedVersion: "2",
		Reason:          resetReason,
	})
	if err != nil || resetResp.JSON200 == nil || resetResp.JSON200.Version != "3" || *resetResp.JSON200.Source != sdk.Global || len(resetResp.JSON200.Conditions) != 3 {
		t.Fatalf("reset school override failed: %+v, err=%v", resetResp, err)
	}

	// Replay with identical Idempotency-Key returns same restored Rule without extra audit rows.
	replayReset, err := client.ResetEligibilityRuleWithResponse(ctx, "buaa", sdk.ResourcesUpload, &sdk.ResetEligibilityRuleParams{IdempotencyKey: "buaa-reset-1"}, sdk.Reset{
		ExpectedVersion: "2",
		Reason:          resetReason,
	})
	if err != nil || replayReset.JSON200 == nil || replayReset.JSON200.Version != "3" {
		t.Fatalf("reset replay failed: %+v, err=%v", replayReset, err)
	}

	// CachedRules immediately reflects the reset on subsequent reads.
	effectiveAfterReset, err := cached.Effective(ctx, "resources.upload", "buaa")
	if err != nil || effectiveAfterReset.Source != e.SourceGlobal || !slices.Equal(effectiveAfterReset.Conditions, []e.Condition{e.Campus, e.Phone, e.Identity}) {
		t.Fatalf("cache did not invalidate on reset: %+v, err=%v", effectiveAfterReset, err)
	}

	// Verify platform.audit stored reasons for both set and reset.
	var storedSetReason, storedResetReason string
	if err = owner.QueryRow(`SELECT reason FROM platform.audit WHERE action='eligibility.set' AND target='buaa:resources.upload'`).Scan(&storedSetReason); err != nil || storedSetReason != buaaReason {
		t.Fatalf("unexpected set audit reason: %q, err=%v", storedSetReason, err)
	}
	if err = owner.QueryRow(`SELECT reason FROM platform.audit WHERE action='eligibility.reset' AND target='buaa:resources.upload'`).Scan(&storedResetReason); err != nil || storedResetReason != resetReason {
		t.Fatalf("unexpected reset audit reason: %q, err=%v", storedResetReason, err)
	}
}
