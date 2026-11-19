package eligibility_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	e "github.com/StuHelper/stuhelper-platform/eligibility"
	"github.com/StuHelper/stuhelper-platform/eligibility/httpapi"
	sdk "github.com/StuHelper/stuhelper-platform/sdk"
)

type stubFacts struct {
	values       map[e.Condition]e.Fact
	rejectEnroll bool
	failAll      bool
	calls        int
}

func (f *stubFacts) Facts(_ context.Context, _, _ string, conditions []e.Condition) (map[e.Condition]e.Fact, error) {
	f.calls++
	if f.failAll {
		return nil, errors.New("verification service temporarily unreachable")
	}
	if f.rejectEnroll && slices.Contains(conditions, e.Enrollment) {
		return nil, errors.New("no single course context in general preflight")
	}
	out := map[e.Condition]e.Fact{}
	for _, c := range conditions {
		if v, ok := f.values[c]; ok {
			out[c] = v
		}
	}
	return out, nil
}

func TestEligibilityPreflightBatchAndDegradedFacts(t *testing.T) {
	ctx := context.Background()
	facts := &stubFacts{
		values: map[e.Condition]e.Fact{
			e.Campus: e.Satisfied,
			e.Phone:  e.Unsatisfied,
		},
		rejectEnroll: true,
	}
	adm := e.Admission{School: "buaa", Rules: e.Defaults{}, Facts: facts}

	mux := http.NewServeMux()
	httpapi.Handler{
		Authenticate: func(r *http.Request) (string, error) {
			if r.Header.Get("Authorization") == "" {
				return "", httpapi.ErrUnauthenticated
			}
			return "urn:issuer:student-1", nil
		},
		Authorize: func(context.Context, string, string) error {
			t.Fatal("user preflight must not require administrator authorization")
			return nil
		},
		Admission: &adm,
	}.Register(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	authClient, err := sdk.NewClientWithResponses(server.URL, sdk.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer session")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	anonClient, err := sdk.NewClientWithResponses(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Unauthenticated preflight returns 401.
	anonResp, err := anonClient.CheckEligibilityActionsWithResponse(ctx, &sdk.CheckEligibilityActionsParams{
		School:  "buaa",
		Actions: []sdk.Action{sdk.ResourcesUpload},
	})
	if err != nil || anonResp.StatusCode() != 401 {
		t.Fatalf("expected 401 for anonymous preflight, got %+v, err=%v", anonResp, err)
	}

	// Cross-school preflight returns 403.
	crossResp, err := authClient.CheckEligibilityActionsWithResponse(ctx, &sdk.CheckEligibilityActionsParams{
		School:  "other-school",
		Actions: []sdk.Action{sdk.ResourcesUpload},
	})
	if err != nil || crossResp.StatusCode() != 403 {
		t.Fatalf("expected 403 for cross-school preflight, got %+v, err=%v", crossResp, err)
	}

	// Batch preflight returns satisfied, missing, and course-specific unavailable conditions in one request.
	resp, err := authClient.CheckEligibilityActionsWithResponse(ctx, &sdk.CheckEligibilityActionsParams{
		School:  "buaa",
		Actions: []sdk.Action{sdk.ResourcesPurchase, sdk.ResourcesUpload, sdk.PublishCourse, sdk.ReplyTeacher},
	})
	if err != nil || resp.JSON200 == nil || len(resp.JSON200.Items) != 4 {
		t.Fatalf("batch preflight failed: %+v, err=%v", resp, err)
	}
	byAction := map[sdk.Action]sdk.EligibilityEvaluation{}
	for _, item := range resp.JSON200.Items {
		byAction[item.Action] = item
	}

	// resources.purchase only requires campus_eligibility -> allowed=true!
	if !byAction[sdk.ResourcesPurchase].Allowed || !slices.Equal(byAction[sdk.ResourcesPurchase].SatisfiedConditions, []sdk.Condition{sdk.ConditionCampusEligibility}) {
		t.Fatalf("unexpected resources.purchase evaluation: %+v", byAction[sdk.ResourcesPurchase])
	}
	// resources.upload requires campus_eligibility + phone_bound -> missing=["phone_bound"]!
	if byAction[sdk.ResourcesUpload].Allowed || !slices.Equal(byAction[sdk.ResourcesUpload].MissingConditions, []sdk.Condition{sdk.ConditionPhoneBound}) {
		t.Fatalf("unexpected resources.upload evaluation: %+v", byAction[sdk.ResourcesUpload])
	}
	// publish_course isolates course_enrollment unavailability without hiding missing phone_bound!
	if byAction[sdk.PublishCourse].Allowed ||
		!slices.Equal(byAction[sdk.PublishCourse].SatisfiedConditions, []sdk.Condition{sdk.ConditionCampusEligibility}) ||
		!slices.Equal(byAction[sdk.PublishCourse].MissingConditions, []sdk.Condition{sdk.ConditionPhoneBound}) ||
		!slices.Equal(byAction[sdk.PublishCourse].UnavailableConditions, []sdk.Condition{sdk.ConditionCourseEnrollment}) {
		t.Fatalf("unexpected publish_course evaluation: %+v", byAction[sdk.PublishCourse])
	}
	// reply_teacher requires phone_bound -> missing=["phone_bound"].
	if byAction[sdk.ReplyTeacher].Allowed || !slices.Equal(byAction[sdk.ReplyTeacher].MissingConditions, []sdk.Condition{sdk.ConditionPhoneBound}) {
		t.Fatalf("unexpected reply_teacher evaluation: %+v", byAction[sdk.ReplyTeacher])
	}

	// When downstream fact provider is temporarily down, preflight reports unavailable_conditions
	// rather than falsely claiming the user lacks campus_eligibility.
	facts.failAll = true
	degradedResp, err := authClient.CheckEligibilityActionsWithResponse(ctx, &sdk.CheckEligibilityActionsParams{
		School:  "buaa",
		Actions: []sdk.Action{sdk.ResourcesPurchase},
	})
	if err != nil || degradedResp.JSON200 == nil || len(degradedResp.JSON200.Items) != 1 {
		t.Fatalf("degraded preflight failed: %+v, err=%v", degradedResp, err)
	}
	item := degradedResp.JSON200.Items[0]
	if item.Allowed || len(item.MissingConditions) != 0 || !slices.Equal(item.UnavailableConditions, []sdk.Condition{sdk.ConditionCampusEligibility}) {
		t.Fatalf("expected unavailable_conditions during outage, got %+v", item)
	}
}
