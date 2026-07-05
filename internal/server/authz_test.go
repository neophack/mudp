package server

import (
	"testing"

	"mudp/internal/store"
)

func TestRoleRankOrder(t *testing.T) {
	cases := []struct {
		role string
		want int
	}{
		{store.RoleReadonly, rankReadonly},
		{store.RoleHelpdesk, rankHelpdesk},
		{store.RoleUser, rankUser},
		{store.RoleOperator, rankOperator},
		{store.RoleAdmin, rankAdmin},
		{"", 0},
		{"bogus", 0},
	}
	for _, c := range cases {
		if got := roleRank(c.role); got != c.want {
			t.Errorf("roleRank(%q) = %d, want %d", c.role, got, c.want)
		}
	}
	// Strict ordering: each tier must outrank the one below it.
	if !(rankReadonly < rankHelpdesk && rankHelpdesk < rankUser && rankUser < rankOperator && rankOperator < rankAdmin) {
		t.Fatal("role ranks are not strictly ordered")
	}
}

func TestCanMutate(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{store.RoleAdmin, true},
		{store.RoleOperator, true},
		{store.RoleUser, true},
		{store.RoleHelpdesk, false},
		{store.RoleReadonly, false},
	}
	for _, c := range cases {
		u := &store.User{Role: c.role}
		if got := canMutate(u); got != c.want {
			t.Errorf("canMutate(%q) = %v, want %v", c.role, got, c.want)
		}
	}
	// nil and unknown roles cannot mutate.
	if canMutate(nil) {
		t.Error("canMutate(nil) = true")
	}
	if canMutate(&store.User{Role: "ghost"}) {
		t.Error("canMutate(unknown) = true")
	}
}
