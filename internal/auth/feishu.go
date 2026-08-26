package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// feishuHost is a var so tests can point the client at a stub server.
var feishuHost = "https://open.feishu.cn"

const (
	pathAppToken      = "/open-apis/auth/v3/app_access_token/internal"
	pathTenantToken   = "/open-apis/auth/v3/tenant_access_token/internal"
	pathUserTokenOIDC = "/open-apis/authen/v1/oidc/access_token"
	pathUserInfo      = "/open-apis/authen/v1/user_info"
	pathSendMessage   = "/open-apis/im/v1/messages"
)

// FeishuUser is the profile returned after a successful OIDC login.
type FeishuUser struct {
	OpenID          string
	Name            string
	Comment         string
	AvatarURL       string
	Email           string
	EnterpriseEmail string
	Mobile          string
	TenantKey       string
	TenantName      string
	DepartmentName  string
}

// Username returns a login-safe username derived from the OpenID by keeping
// only letters and digits. This avoids non-ASCII names and special characters
// in system identifiers.
func (fu FeishuUser) Username() string {
	if fu.OpenID == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range fu.OpenID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "feishu-" + fu.OpenID
	}
	return b.String()
}

// FeishuClient performs the OIDC code-for-token exchange and user info lookup.
type FeishuClient struct {
	appID     string
	appSecret string
	http      *http.Client
}

func NewFeishuClient(appID, appSecret string) *FeishuClient {
	return &FeishuClient{
		appID:     appID,
		appSecret: appSecret,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

// AuthorizeURL builds the redirect URL users are sent to in order to consent.
// redirectURI must be registered in the Feishu app console.
func (f *FeishuClient) AuthorizeURL(redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", f.appID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)
	return feishuHost + "/open-apis/authen/v1/index?" + q.Encode()
}

// appAccessToken fetches a tenant-scoped token used to exchange the user code.
func (f *FeishuClient) appAccessToken(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"app_id":     f.appID,
		"app_secret": f.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+pathAppToken, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var resp struct {
		Code           int    `json:"code"`
		Msg            string `json:"msg"`
		AppAccessToken string `json:"app_access_token"`
	}
	if err := f.do(req, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 || resp.AppAccessToken == "" {
		return "", fmt.Errorf("feishu app_access_token failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.AppAccessToken, nil
}

// ExchangeCode trades the OAuth code for a user access token.
func (f *FeishuClient) ExchangeCode(ctx context.Context, code string) (string, error) {
	appToken, err := f.appAccessToken(ctx)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+pathUserTokenOIDC, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+appToken)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := f.do(req, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 || resp.Data.AccessToken == "" {
		return "", fmt.Errorf("feishu access_token failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.AccessToken, nil
}

// UserInfo fetches the logged-in user's profile using their access token.
func (f *FeishuClient) UserInfo(ctx context.Context, accessToken string) (FeishuUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feishuHost+pathUserInfo, nil)
	if err != nil {
		return FeishuUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// Only open_id/name/tenant fields are parsed here — enterprise email,
	// personal email, mobile, avatar, and department are deliberately not
	// requested from the profile so they never enter the app.
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID     string `json:"open_id"`
			Name       string `json:"name"`
			TenantKey  string `json:"tenant_key"`
			TenantName string `json:"tenant_name"`
		} `json:"data"`
	}
	if err := f.do(req, &resp); err != nil {
		return FeishuUser{}, err
	}
	if resp.Code != 0 {
		return FeishuUser{}, fmt.Errorf("feishu user_info failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data.OpenID == "" {
		return FeishuUser{}, errors.New("feishu returned empty open_id")
	}
	return FeishuUser{
		OpenID:     resp.Data.OpenID,
		Name:       strings.TrimSpace(resp.Data.Name),
		Comment:    strings.TrimSpace(resp.Data.Name),
		TenantKey:  strings.TrimSpace(resp.Data.TenantKey),
		TenantName: strings.TrimSpace(resp.Data.TenantName),
	}, nil
}

// TenantAccessToken fetches a tenant-scoped token for server APIs such as
// sending bot messages. Feishu returns the same token while it has more than
// 30 minutes left, so fetching one per send is safe without a local cache.
func (f *FeishuClient) TenantAccessToken(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"app_id":     f.appID,
		"app_secret": f.appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+pathTenantToken, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := f.do(req, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 || resp.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu tenant_access_token failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.TenantAccessToken, nil
}

// SendText delivers a plain-text bot message to one user by their open_id.
// The app needs the bot capability and one of the im:message scopes, and the
// recipient must be inside the app's availability range.
func (f *FeishuClient) SendText(ctx context.Context, openID, text string) error {
	token, err := f.TenantAccessToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	body, _ := json.Marshal(map[string]string{
		"receive_id": openID,
		"msg_type":   "text",
		"content":    string(content),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feishuHost+pathSendMessage+"?receive_id_type=open_id", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := f.do(req, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu send failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (f *FeishuClient) do(req *http.Request, out any) error {
	res, err := f.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("feishu read body: %w", err)
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("feishu http %d: %s", res.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}
