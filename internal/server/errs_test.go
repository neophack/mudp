package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyErr(t *testing.T) {
	cases := []struct {
		msg, want string
	}{
		{"Error response from daemon: Cannot connect to the Docker daemon at unix:///var/run/docker.sock", "docker_unavailable"},
		{"docker unavailable", "docker_unavailable"},
		{"container is not yours", "not_owner"},
		{"upload would exceed netdisk quota", "quota_exceeded"},
		{"context deadline exceeded", "timeout"},
		{"request timed out waiting for log stream", "timeout"},
		// Unmatched messages carry no code and are surfaced verbatim.
		{"no such image: nginx:latest", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := classifyErr(c.msg); got != c.want {
			t.Errorf("classifyErr(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
}

func TestWriteErrIncludesCode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusServiceUnavailable, "docker unavailable")
	var out struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error != "docker unavailable" || out.Code != "docker_unavailable" {
		t.Fatalf("payload = %s, want error + docker_unavailable code", rec.Body.String())
	}

	// Unmatched messages keep the error field only.
	rec = httptest.NewRecorder()
	writeErr(rec, http.StatusBadRequest, "no such image")
	var plain map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &plain); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := plain["code"]; has {
		t.Fatalf("unmatched message should carry no code: %s", rec.Body.String())
	}
}
