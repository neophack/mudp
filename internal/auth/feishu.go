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

const (
	feishuHost        = "https://open.feishu.cn"
	pathAppToken      = "/open-apis/auth/v3/app_access_token/internal"
	pathUserTokenOIDC = "/open-apis/authen/v1/oidc/access_token"
	pathUserInfo      = "/open-apis/authen/v1/user_info"
)

// FeishuUser is the minimal profile returned after a successful OIDC login.
type FeishuUser struct {
	OpenID string
	Name   string
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
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID string `json:"open_id"`
			Name   string `json:"name"`
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
	return FeishuUser{OpenID: resp.Data.OpenID, Name: strings.TrimSpace(resp.Data.Name)}, nil
}

func (f *FeishuClient) do(req *http.Request, out any) error {
	res, err := f.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return fmt.Errorf("feishu http %d: %s", res.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}
