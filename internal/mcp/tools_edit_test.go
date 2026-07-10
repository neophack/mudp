package mcp

import (
	"strings"
	"testing"
)

func TestApplyEdits_SingleReplace(t *testing.T) {
	in := "hello world\nfoo bar"
	got, err := applyEdits(in, []editRequest{{OldText: "world", NewText: "there"}}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "hello there\nfoo bar"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyEdits_MultipleOrdered(t *testing.T) {
	in := "alpha\nbeta\ngamma"
	got, err := applyEdits(in, []editRequest{
		{OldText: "alpha", NewText: "A"},
		{OldText: "gamma", NewText: "G"},
	}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "A\nbeta\nG"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyEdits_NotFound(t *testing.T) {
	_, err := applyEdits("hello", []editRequest{{OldText: "missing", NewText: "x"}}, false)
	if err == nil {
		t.Fatal("expected error for missing oldText")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %v", err)
	}
}

func TestApplyEdits_AmbiguousWithoutReplaceAll(t *testing.T) {
	// "x" appears twice → must error when replaceAll is false.
	_, err := applyEdits("x and x", []editRequest{{OldText: "x", NewText: "y"}}, false)
	if err == nil {
		t.Fatal("expected error for non-unique match")
	}
	if !strings.Contains(err.Error(), "matches 2 times") {
		t.Errorf("error should report match count, got: %v", err)
	}
}

func TestApplyEdits_ReplaceAll(t *testing.T) {
	got, err := applyEdits("x and x", []editRequest{{OldText: "x", NewText: "y"}}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "y and y" {
		t.Errorf("got %q, want %q", got, "y and y")
	}
}

func TestApplyEdits_ReplaceAllStillRequiresMatch(t *testing.T) {
	_, err := applyEdits("nothing here", []editRequest{{OldText: "x", NewText: "y"}}, true)
	if err == nil {
		t.Fatal("replaceAll should still require at least one match")
	}
}

func TestApplyEdits_EmptyOldText(t *testing.T) {
	_, err := applyEdits("hello", []editRequest{{OldText: "", NewText: "x"}}, false)
	if err == nil {
		t.Fatal("expected error for empty oldText")
	}
}

func TestApplyEdits_NoEdits(t *testing.T) {
	_, err := applyEdits("hello", nil, false)
	if err == nil {
		t.Fatal("expected error when no edits are given")
	}
}

func TestApplyEdits_EditCanTargetResultOfPrevious(t *testing.T) {
	// After the first edit changes A→AB, the second edit should find the new AB.
	in := "A"
	got, err := applyEdits(in, []editRequest{
		{OldText: "A", NewText: "AB"},
		{OldText: "AB", NewText: "ABC"},
	}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ABC" {
		t.Errorf("got %q, want %q", got, "ABC")
	}
}
