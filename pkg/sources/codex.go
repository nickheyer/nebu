package sources

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"golang.org/x/oauth2"
)

// The sign-in OpenAI's Codex makes with a ChatGPT plan, taken from its published client:
// https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/manager.rs (client id, token endpoint),
// https://github.com/openai/codex/blob/main/codex-rs/login/src/device_code_auth.rs (device code flow),
// https://github.com/openai/codex/blob/main/codex-rs/login/src/oauth/client.rs (code exchange and refresh fields)
const (
	codexIssuer   = "https://auth.openai.com"
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// The device code lapses after fifteen minutes, as the client says when it shows one
	codexDeviceTTL = 15 * time.Minute
)

// The models a ChatGPT plan includes, served under the Codex endpoint over the Responses API:
// https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs (CHATGPT_CODEX_BASE_URL), with the
// workspace header of https://github.com/openai/codex/blob/main/codex-rs/http-client/src/openai_headers.rs, the
// originator of https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/default_client.rs, and refreshes sent
// as JSON as https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/manager.rs sends them. The endpoint serves
// /responses alone, and Codex sends it every request streamed, stored false and with instructions, so a request bringing
// none goes with a plain one rather than without
var codex = func() *Catalog {
	cat := modelCatalog(v1.SourceKind_SOURCE_KIND_CODEX, "codex", "Codex", "https://chatgpt.com/backend-api/codex", v1.ApiFlavor_API_FLAVOR_OPENAI, &SignIn{
		Header: "Authorization",
		Scheme: "Bearer",
		Fixed:  map[string]string{"originator": "codex_cli_rs"},
		OAuth:  &OAuth{Config: *codexOAuth, JSON: true, Device: codexDevice, Claims: codexClaims, Responses: &Responses{Instructions: "You are a helpful assistant."}},
	})
	cat.Description = "Models a ChatGPT plan includes through OpenAI's Codex service, signed in to as Codex does, served over the Responses API"
	return cat
}()

func init() { register(codex) }

// The client a device sign-in's code is exchanged and its tokens refreshed under, the redirect the device flow names
var codexOAuth = &oauth2.Config{
	ClientID:    codexClientID,
	Endpoint:    oauth2.Endpoint{AuthURL: codexIssuer + "/oauth/authorize", TokenURL: codexIssuer + "/oauth/token", AuthStyle: oauth2.AuthStyleInParams},
	RedirectURL: codexIssuer + "/deviceauth/callback",
}

// Asks for a user code the person enters at the Codex device page, the wait trading the code it yields for tokens
func codexDevice(ctx context.Context, client *http.Client) (*Device, error) {
	var code struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Usercode     string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := codexPost(ctx, client, codexIssuer+"/api/accounts/deviceauth/usercode", map[string]string{"client_id": codexClientID}, &code); err != nil {
		return nil, err
	}
	user := code.UserCode
	if user == "" {
		user = code.Usercode
	}
	interval, _ := strconv.Atoi(strings.Trim(string(code.Interval), `" `))
	every := time.Duration(max(interval, 1)) * time.Second
	expires := time.Now().Add(codexDeviceTTL)
	wait := func(ctx context.Context) (*oauth2.Token, error) {
		for {
			var issued struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeVerifier      string `json:"code_verifier"`
			}
			status, err := codexPoll(ctx, client, map[string]string{"device_auth_id": code.DeviceAuthID, "user_code": user}, &issued)
			switch {
			case err != nil:
				return nil, err
			case status == http.StatusOK:
				return codexOAuth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, client), issued.AuthorizationCode, oauth2.VerifierOption(issued.CodeVerifier))
			case status != http.StatusForbidden && status != http.StatusNotFound:
				return nil, fmt.Errorf("device sign-in answered %d", status)
			case time.Now().After(expires):
				return nil, fmt.Errorf("the code lapsed before it was approved, sign in again")
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(every):
			}
		}
	}
	return &Device{URL: codexIssuer + "/codex/device", Code: user, Expires: expires, Wait: wait}, nil
}

// Posts JSON, decoding a successful answer
func codexPost(ctx context.Context, client *http.Client, url string, body, out any) error {
	status, err := postJSON(ctx, client, url, body, out)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("%s answered %d", url, status)
	}
	return err
}

// Asks whether the person approved, a 403 or 404 meaning not yet
func codexPoll(ctx context.Context, client *http.Client, body, out any) (int, error) {
	return postJSON(ctx, client, codexIssuer+"/api/accounts/deviceauth/token", body, out)
}

// Posts JSON, decoding a successful answer and answering the status
func postJSON(ctx context.Context, client *http.Client, url string, body, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return 0, fmt.Errorf("decode %s: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}

// The workspace the id token names, which the Codex endpoint takes in its own header
func codexClaims(tok *oauth2.Token) map[string]string {
	idToken, _ := tok.Extra("id_token").(string)
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims struct {
		Auth struct {
			Workspace string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Auth.Workspace == "" {
		return nil
	}
	return map[string]string{"chatgpt-account-id": claims.Auth.Workspace}
}
