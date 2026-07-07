package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
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

func PromptAuditEnabled() bool {
	if !common.GetEnvOrDefaultBool("PROMPT_AUDIT_ENABLED", false) {
		return false
	}
	return strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_ENDPOINT", "")) != "" &&
		len(promptAuditKeywords()) > 0
}

func SubmitPromptAuditIfMatched(prompt string, meta PromptAuditMeta) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	keywords := promptAuditKeywords()
	if len(keywords) == 0 {
		return
	}

	matched := matchPromptAuditKeywords(prompt, keywords)
	if len(matched) == 0 {
		return
	}

	payload := buildPromptAuditPayload(prompt, matched, meta)
	go postPromptAudit(payload)
}

func promptAuditKeywords() []string {
	raw := common.GetEnvOrDefaultString("PROMPT_AUDIT_KEYWORDS", "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.NewReplacer(",", "\n", "，", "\n", ";", "\n", "；", "\n").Replace(raw)

	parts := strings.Split(raw, "\n")
	keywords := make([]string, 0, len(parts))
	for _, part := range parts {
		keyword := strings.ToLower(strings.TrimSpace(part))
		if keyword == "" {
			continue
		}
		keywords = append(keywords, keyword)
	}
	return keywords
}

func matchPromptAuditKeywords(prompt string, keywords []string) []string {
	text := strings.ToLower(prompt)
	matched := make([]string, 0, 4)
	seen := make(map[string]struct{})
	for _, keyword := range keywords {
		if _, ok := seen[keyword]; ok {
			continue
		}
		if strings.Contains(text, keyword) {
			matched = append(matched, keyword)
			seen[keyword] = struct{}{}
		}
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
