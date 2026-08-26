package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newStubFeishu(t *testing.T, handler http.HandlerFunc) *FeishuClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := feishuHost
	feishuHost = srv.URL
	t.Cleanup(func() { feishuHost = old })
	return NewFeishuClient("cli_test", "secret")
}

func TestFeishuClientSendText(t *testing.T) {
	var gotAuth, gotQuery string
	var body map[string]string
	var content map[string]string
	c := newStubFeishu(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathTenantToken:
			w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-123","expire":7200}`))
		case pathSendMessage:
			gotAuth = r.Header.Get("Authorization")
			gotQuery = r.URL.RawQuery
			json.NewDecoder(r.Body).Decode(&body)
			json.Unmarshal([]byte(body["content"]), &content)
			w.Write([]byte(`{"code":0,"msg":"success","data":{"message_id":"om_1"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.SendText(ctx, "ou_abc", "hello"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if gotAuth != "Bearer t-123" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotQuery, "receive_id_type=open_id") {
		t.Fatalf("query = %q, want receive_id_type=open_id", gotQuery)
	}
	if body["receive_id"] != "ou_abc" || body["msg_type"] != "text" {
		t.Fatalf("request body = %+v", body)
	}
	if content["text"] != "hello" {
		t.Fatalf("content = %+v", content)
	}
}

func TestFeishuClientSendTextErrorBody(t *testing.T) {
	c := newStubFeishu(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathTenantToken {
			w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-123","expire":7200}`))
			return
		}
		// Feishu answers HTTP 200 with an error body (e.g. user outside the
		// bot's availability range).
		w.Write([]byte(`{"code":230013,"msg":"Bot has NO availability to this user."}`))
	})
	if err := c.SendText(context.Background(), "ou_x", "hi"); err == nil || !strings.Contains(err.Error(), "230013") {
		t.Fatalf("expected Feishu error-body rejection, got %v", err)
	}
}

func TestFeishuClientTenantAccessTokenFailure(t *testing.T) {
	c := newStubFeishu(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":10003,"msg":"app_secret invalid"}`))
	})
	if _, err := c.TenantAccessToken(context.Background()); err == nil || !strings.Contains(err.Error(), "10003") {
		t.Fatalf("expected token failure, got %v", err)
	}
}
