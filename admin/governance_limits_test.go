package admin

import (
	"context"
	"errors"
	"testing"
)

func TestAdministratorGrantsSharePageLimitAndHonorCancellation(t *testing.T) {
	group := Scope{School: "*", Group: "group", Module: "*"}
	rows := func() []AdministratorSummary {
		grant := Grant{Role: Role{ID: "reader"}, Scope: Global}
		return []AdministratorSummary{{Grants: []Grant{grant}}, {Grants: []Grant{grant}}}
	}
	permitted := []Scope{group, group}
	if err := narrowAdministratorGrants(context.Background(), rows(), permitted, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected page limit, got %v", err)
	}
	atLimit := rows()
	if err := narrowAdministratorGrants(context.Background(), atLimit, permitted, 2); err != nil || len(atLimit[1].Grants) != 1 || atLimit[1].Grants[0].Scope != group {
		t.Fatalf("at limit: %+v %v", atLimit, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := narrowAdministratorGrants(ctx, rows(), permitted, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
