package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	goahocorasick "github.com/anknown/ahocorasick"
)

const promptAuditSource = "new-api"

type PromptAuditMeta struct {
	RequestId string
	UserId    int
	TokenId   int
	Model     string
	Group     string
	ChannelId int
}

type promptAuditPayload struct {
	Source          string   `json:"source"`
	RequestId       string   `json:"request_id"`
	UserId          int      `json:"user_id"`
	TokenId         int      `json:"token_id"`
	Model           string   `json:"model"`
	Group           string   `json:"group"`
	ChannelId       int      `json:"channel_id"`
	Prompt          string   `json:"prompt"`
	PromptSHA256    string   `json:"prompt_sha256"`
	PromptTruncated bool     `json:"prompt_truncated"`
	MatchedKeywords []string `json:"matched_keywords"`
	CreatedAt       int64    `json:"created_at"`
}

type promptAuditRemoteConfig struct {
	Enabled   bool     `json:"enabled"`
	Version   int64    `json:"version"`
	Keywords  []string `json:"keywords"`
	UpdatedAt int64    `json:"updated_at"`
}

type promptAuditRemoteConfigVersion struct {
	Enabled   bool  `json:"enabled"`
	Version   int64 `json:"version"`
	UpdatedAt int64 `json:"updated_at"`
	Count     int   `json:"count"`
}

type promptAuditRemoteConfigResponse struct {
	Success bool                    `json:"success"`
	Data    promptAuditRemoteConfig `json:"data"`
}

type promptAuditRemoteVersionResponse struct {
	Success bool                           `json:"success"`
	Data    promptAuditRemoteConfigVersion `json:"data"`
}

type promptAuditMatcherState struct {
	loaded    bool
	enabled   bool
	version   int64
	updatedAt int64
	keywords  []string
	matcher   *goahocorasick.Machine
}

var promptAuditMatcher struct {
	sync.RWMutex
	state promptAuditMatcherState
}

func PromptAuditEnabled() bool {
	if !common.GetEnvOrDefaultBool("PROMPT_AUDIT_ENABLED", false) {
		return false
	}
	if strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_ENDPOINT", "")) == "" {
		return false
	}
	if strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_CONFIG_ENDPOINT", "")) != "" {
		return true
	}
	return len(promptAuditEnvKeywords()) > 0
}

func SubmitPromptAuditIfMatched(prompt string, meta PromptAuditMeta) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	matched := matchPromptAuditKeywords(prompt)
	if len(matched) == 0 {
		return
	}

	payload := buildPromptAuditPayload(prompt, matched, meta)
	go postPromptAudit(payload)
}

func StartPromptAuditConfigSyncTask() {
	if !common.GetEnvOrDefaultBool("PROMPT_AUDIT_ENABLED", false) {
		return
	}
	if strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_CONFIG_ENDPOINT", "")) == "" {
		return
	}

	go func() {
		syncPromptAuditConfig(true)

		intervalSeconds := common.GetEnvOrDefault("PROMPT_AUDIT_CONFIG_SYNC_INTERVAL_SECONDS", 86400)
		if intervalSeconds < 60 {
			intervalSeconds = 60
		}

		ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			syncPromptAuditConfig(false)
		}
	}()
}

func syncPromptAuditConfig(force bool) {
	if !force {
		remoteVersion, err := fetchPromptAuditConfigVersion()
		if err == nil {
			promptAuditMatcher.RLock()
			currentVersion := promptAuditMatcher.state.version
			promptAuditMatcher.RUnlock()
			if remoteVersion.Version == currentVersion {
				return
			}
		}
	}

	cfg, err := fetchPromptAuditConfig()
	if err != nil {
		common.SysError("prompt audit config sync failed: " + err.Error())
		return
	}
	applyPromptAuditRemoteConfig(cfg)
}

func fetchPromptAuditConfigVersion() (promptAuditRemoteConfigVersion, error) {
	endpoint := strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_CONFIG_VERSION_ENDPOINT", ""))
	if endpoint == "" {
		configEndpoint := strings.TrimRight(strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_CONFIG_ENDPOINT", "")), "/")
		if configEndpoint == "" {
			return promptAuditRemoteConfigVersion{}, fmt.Errorf("PROMPT_AUDIT_CONFIG_ENDPOINT is empty")
		}
		endpoint = configEndpoint + "-version"
	}

	var resp promptAuditRemoteVersionResponse
	if err := getPromptAuditJSON(endpoint, &resp); err != nil {
		return promptAuditRemoteConfigVersion{}, err
	}
	if !resp.Success {
		return promptAuditRemoteConfigVersion{}, fmt.Errorf("prompt audit version response success=false")
	}
	return resp.Data, nil
}

func fetchPromptAuditConfig() (promptAuditRemoteConfig, error) {
	endpoint := strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_CONFIG_ENDPOINT", ""))
	if endpoint == "" {
		return promptAuditRemoteConfig{}, fmt.Errorf("PROMPT_AUDIT_CONFIG_ENDPOINT is empty")
	}

	var resp promptAuditRemoteConfigResponse
	if err := getPromptAuditJSON(endpoint, &resp); err != nil {
		return promptAuditRemoteConfig{}, err
	}
	if !resp.Success {
		return promptAuditRemoteConfig{}, fmt.Errorf("prompt audit config response success=false")
	}
	return resp.Data, nil
}

func getPromptAuditJSON(endpoint string, out interface{}) error {
	timeoutMs := common.GetEnvOrDefault("PROMPT_AUDIT_CONFIG_TIMEOUT_MS", 1500)
	if timeoutMs <= 0 {
		timeoutMs = 1500
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if secret := common.GetEnvOrDefaultString("PROMPT_AUDIT_SECRET", ""); secret != "" {
		req.Header.Set("X-Prompt-Audit-Secret", secret)
	}

	client := http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func applyPromptAuditRemoteConfig(cfg promptAuditRemoteConfig) {
	keywords := normalizePromptAuditKeywords(cfg.Keywords)
	var matcher *goahocorasick.Machine
	if cfg.Enabled && len(keywords) > 0 {
		matcher = InitAc(keywords)
		if matcher == nil {
			common.SysError("prompt audit config sync failed: build Aho-Corasick matcher returned nil")
			return
		}
	}

	promptAuditMatcher.Lock()
	promptAuditMatcher.state = promptAuditMatcherState{
		loaded:    true,
		enabled:   cfg.Enabled,
		version:   cfg.Version,
		updatedAt: cfg.UpdatedAt,
		keywords:  keywords,
		matcher:   matcher,
	}
	promptAuditMatcher.Unlock()

	common.SysLog(fmt.Sprintf("prompt audit config synced: enabled=%v version=%d keywords=%d", cfg.Enabled, cfg.Version, len(keywords)))
}

func promptAuditEnvKeywords() []string {
	raw := common.GetEnvOrDefaultString("PROMPT_AUDIT_KEYWORDS", "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.NewReplacer(",", "\n", "，", "\n", ";", "\n", "；", "\n").Replace(raw)
	return normalizePromptAuditKeywords(strings.Split(raw, "\n"))
}

func normalizePromptAuditKeywords(parts []string) []string {
	keywords := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		keyword := strings.ToLower(strings.TrimSpace(part))
		if keyword == "" {
			continue
		}
		if _, ok := seen[keyword]; ok {
			continue
		}
		seen[keyword] = struct{}{}
		keywords = append(keywords, keyword)
	}
	return keywords
}

func matchPromptAuditKeywords(prompt string) []string {
	text := strings.ToLower(prompt)

	promptAuditMatcher.RLock()
	state := promptAuditMatcher.state
	promptAuditMatcher.RUnlock()
	if state.loaded {
		if state.enabled && state.matcher != nil {
			return matchPromptAuditAC(text, state.matcher)
		}
		return nil
	}

	keywords := promptAuditEnvKeywords()
	if len(keywords) == 0 {
		return nil
	}
	matcher := getOrBuildAC(keywords)
	if matcher == nil {
		return nil
	}
	return matchPromptAuditAC(text, matcher)
}

func matchPromptAuditAC(text string, matcher *goahocorasick.Machine) []string {
	hits := matcher.MultiPatternSearch([]rune(text), false)
	if len(hits) == 0 {
		return nil
	}

	matched := make([]string, 0, len(hits))
	seen := make(map[string]struct{})
	for _, hit := range hits {
		word := string(hit.Word)
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		matched = append(matched, word)
	}
	return matched
}

func buildPromptAuditPayload(prompt string, matched []string, meta PromptAuditMeta) promptAuditPayload {
	hash := sha256.Sum256([]byte(prompt))
	maxBytes := common.GetEnvOrDefault("PROMPT_AUDIT_MAX_PROMPT_BYTES", 65536)
	truncated := false
	if maxBytes > 0 && len(prompt) > maxBytes {
		prompt = truncateUTF8ByBytes(prompt, maxBytes)
		truncated = true
	}

	return promptAuditPayload{
		Source:          promptAuditSource,
		RequestId:       meta.RequestId,
		UserId:          meta.UserId,
		TokenId:         meta.TokenId,
		Model:           meta.Model,
		Group:           meta.Group,
		ChannelId:       meta.ChannelId,
		Prompt:          prompt,
		PromptSHA256:    hex.EncodeToString(hash[:]),
		PromptTruncated: truncated,
		MatchedKeywords: matched,
		CreatedAt:       time.Now().Unix(),
	}
}

func truncateUTF8ByBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	if cut <= 0 {
		return ""
	}
	return s[:cut]
}

func postPromptAudit(payload promptAuditPayload) {
	endpoint := strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_ENDPOINT", ""))
	if endpoint == "" {
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		common.SysError("prompt audit marshal failed: " + err.Error())
		return
	}

	timeoutMs := common.GetEnvOrDefault("PROMPT_AUDIT_TIMEOUT_MS", 800)
	if timeoutMs <= 0 {
		timeoutMs = 800
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		common.SysError("prompt audit request build failed: " + err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := common.GetEnvOrDefaultString("PROMPT_AUDIT_SECRET", ""); secret != "" {
		req.Header.Set("X-Prompt-Audit-Secret", secret)
	}

	client := http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		common.SysError("prompt audit submit failed: " + err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		common.SysError(fmt.Sprintf("prompt audit submit returned status %d", resp.StatusCode))
	}
}
