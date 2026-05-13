package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const fireworksDefaultBaseURL = "https://api.fireworks.ai/inference/v1"

// FireworksExecutor is a thin wrapper around OpenAICompatExecutor for the Fireworks platform.
// It bridges file-based auth (Metadata) to the OpenAI-compat credential resolution (Attributes).
type FireworksExecutor struct {
	inner *OpenAICompatExecutor
	cfg   *config.Config
}

// NewFireworksExecutor creates a new Fireworks executor.
func NewFireworksExecutor(cfg *config.Config) *FireworksExecutor {
	return &FireworksExecutor{
		inner: NewOpenAICompatExecutor("fireworks", cfg),
		cfg:   cfg,
	}
}

// Identifier returns the executor identifier.
func (e *FireworksExecutor) Identifier() string { return "fireworks" }

// prepareAuth clones the auth and bridges credential fields from Metadata into Attributes
// so that the inner OpenAICompatExecutor can resolve them.
func (e *FireworksExecutor) prepareAuth(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	a := auth.Clone()
	if a.Attributes == nil {
		a.Attributes = make(map[string]string)
	}
	if a.Attributes["api_key"] == "" && a.Metadata != nil {
		if v, ok := a.Metadata["api_key"].(string); ok && strings.TrimSpace(v) != "" {
			a.Attributes["api_key"] = v
		}
	}
	if a.Attributes["base_url"] == "" {
		if a.Metadata != nil {
			if v, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
				a.Attributes["base_url"] = v
				return a
			}
		}
		a.Attributes["base_url"] = fireworksDefaultBaseURL
	}
	return a
}

// Execute performs a non-streaming chat completion request via the OpenAI-compatible path.
func (e *FireworksExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.inner.Execute(ctx, e.prepareAuth(auth), req, opts)
}

// ExecuteStream performs a streaming chat completion request via the OpenAI-compatible path.
func (e *FireworksExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.inner.ExecuteStream(ctx, e.prepareAuth(auth), req, opts)
}

// CountTokens estimates token count for Fireworks requests.
func (e *FireworksExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.inner.CountTokens(ctx, e.prepareAuth(auth), req, opts)
}

// Refresh is a no-op for API-key based Fireworks credentials.
func (e *FireworksExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("fireworks executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

// FetchQuota retrieves monthly spend quota from the Fireworks API.
// Returns (used, limit, ok). Called on-demand, not automatically.
func (e *FireworksExecutor) FetchQuota(auth *cliproxyauth.Auth) (used float64, limit float64, ok bool) {
	if auth == nil || auth.Metadata == nil {
		return 0, 0, false
	}
	accountID, _ := auth.Metadata["account_id"].(string)
	if strings.TrimSpace(accountID) == "" {
		return 0, 0, false
	}
	prepared := e.prepareAuth(auth)
	if prepared == nil || prepared.Attributes == nil {
		return
	}
	apiKey := strings.TrimSpace(prepared.Attributes["api_key"])
	if apiKey == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	quotaURL := "https://api.fireworks.ai/v1/accounts/" + accountID + "/quotas"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, quotaURL, nil)
	if err != nil {
		log.Debugf("fireworks executor: failed to create quota request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, prepared, 15*time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Debugf("fireworks executor: failed to fetch quota: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("fireworks executor: quota endpoint returned status %d", resp.StatusCode)
		return
	}

	var result struct {
		Quotas []struct {
			Name    string  `json:"name"`
			Value   string  `json:"value"`
			Usage   float64 `json:"usage"`
		} `json:"quotas"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}

	for _, q := range result.Quotas {
		if strings.HasSuffix(q.Name, "/monthly-spend-usd") {
			l, _ := strconv.ParseFloat(q.Value, 64)
			return q.Usage, l, true
		}
	}
	return 0, 0, false
}

// HttpRequest injects Fireworks credentials into the request and executes it.
func (e *FireworksExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("fireworks executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	prepared := e.prepareAuth(auth)
	httpReq := req.WithContext(ctx)
	if prepared != nil && prepared.Attributes != nil {
		if apiKey := strings.TrimSpace(prepared.Attributes["api_key"]); apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
	var attrs map[string]string
	if prepared != nil {
		attrs = prepared.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, prepared, 0)
	return httpClient.Do(httpReq)
}

// FetchModels retrieves available models from the Fireworks API.
func (e *FireworksExecutor) FetchModels(auth *cliproxyauth.Auth) []*registry.ModelInfo {
	prepared := e.prepareAuth(auth)
	if prepared == nil || prepared.Attributes == nil {
		log.Warnf("fireworks executor: no credentials available for fetching models")
		return nil
	}
	baseURL := strings.TrimSpace(prepared.Attributes["base_url"])
	apiKey := strings.TrimSpace(prepared.Attributes["api_key"])
	if baseURL == "" || apiKey == "" {
		log.Warnf("fireworks executor: missing base_url or api_key (base_url=%q, api_key_set=%v)", baseURL, apiKey != "")
		return nil
	}

	log.Infof("fireworks executor: fetching models from %s", baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	modelsURL := strings.TrimSuffix(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		log.Warnf("fireworks executor: failed to create models request: %v", err)
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, prepared, 30*time.Second)
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Warnf("fireworks executor: failed to fetch models from %s: %v", modelsURL, err)
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("fireworks executor: failed to read models response: %v", err)
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warnf("fireworks executor: models endpoint returned status %d: %s", resp.StatusCode, string(body))
		return nil
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Warnf("fireworks executor: failed to parse models response: %v", err)
		return nil
	}

	models := make([]*registry.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID == "" {
			continue
		}
		models = append(models, &registry.ModelInfo{
			ID:          m.ID,
			Object:      "model",
			Created:     m.Created,
			OwnedBy:     m.OwnedBy,
			Type:        "fireworks",
			DisplayName: m.ID,
		})
	}
	log.Infof("fireworks executor: fetched %d models", len(models))
	return models
}
