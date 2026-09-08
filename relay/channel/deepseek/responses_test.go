package deepseek

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	channelconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesURL(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://api.deepseek.com"}, RelayMode: relayconstant.RelayModeResponses}
	got, err := (&Adaptor{}).GetRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, "https://api.deepseek.com/responses", got)
}

func TestResponsesThinkingSuffix(t *testing.T) {
	for _, tc := range []struct {
		name, model, upstream, wantModel, effort string
		nilInfo, nilMeta, noReasoning            bool
	}{
		{name: "disabled", model: "deepseek-v4-pro-none", wantModel: "deepseek-v4-pro", effort: "none", noReasoning: true},
		{name: "max", model: "deepseek-v4-flash-max", wantModel: "deepseek-v4-flash", effort: "max"},
		{name: "mapped", model: "alias", upstream: "deepseek-v4-pro-max", wantModel: "deepseek-v4-pro", effort: "max"},
		{name: "explicit", model: "deepseek-v4-pro", wantModel: "deepseek-v4-pro", effort: "low"},
		{name: "other_model", model: "other-max", wantModel: "other-max", effort: "low"},
		{name: "nil_info", model: "deepseek-v4-pro-none", wantModel: "deepseek-v4-pro", effort: "none", nilInfo: true},
		{name: "nil_meta", model: "deepseek-v4-pro-max", wantModel: "deepseek-v4-pro", effort: "max", nilMeta: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: tc.upstream}}
			if tc.nilInfo {
				info = nil
			} else if tc.nilMeta {
				info.ChannelMeta = nil
			}
			req := dto.OpenAIResponsesRequest{Model: tc.model, Input: []byte(`"hello"`)}
			if !tc.noReasoning {
				req.Reasoning = &dto.Reasoning{Effort: "low", Summary: "auto"}
			}
			converted, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, req)
			require.NoError(t, err)
			got, ok := converted.(dto.OpenAIResponsesRequest)
			require.True(t, ok)
			assert.Equal(t, tc.wantModel, got.Model)
			require.NotNil(t, got.Reasoning)
			assert.Equal(t, tc.effort, got.Reasoning.Effort)
			if !tc.noReasoning {
				assert.Equal(t, "auto", got.Reasoning.Summary)
			}
			assert.Equal(t, req.Input, got.Input)
			if info != nil {
				assert.Equal(t, tc.effort, info.ReasoningEffort)
			}
			if tc.upstream != "" {
				assert.Equal(t, tc.wantModel, info.UpstreamModelName)
			}
		})
	}
}

func TestResponsesHandlers(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	oldTimeout := channelconstant.StreamingTimeout
	channelconstant.StreamingTimeout = 30
	t.Cleanup(func() { channelconstant.StreamingTimeout = oldTimeout })
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := `{"id":"resp_test","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}`
			contentType := "application/json"
			if stream {
				body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" + "data: {\"type\":\"response.completed\",\"response\":" + body + "}\n\ndata: [DONE]\n\n"
				contentType = "text/event-stream"
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "test-model"}, RelayMode: relayconstant.RelayModeResponses, IsStream: stream, DisablePing: true}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
			result, apiErr := (&Adaptor{}).DoResponse(c, resp, info)
			require.Nil(t, apiErr)
			usage, ok := result.(*dto.Usage)
			require.True(t, ok)
			assert.Equal(t, 7, usage.PromptTokens)
			assert.Equal(t, 3, usage.CompletionTokens)
			assert.Equal(t, 10, usage.TotalTokens)
			if stream {
				assert.Contains(t, recorder.Body.String(), "event: response.output_text.delta")
				assert.Contains(t, recorder.Body.String(), "event: response.completed")
				assert.Contains(t, recorder.Body.String(), `"delta":"hello"`)
				assert.Less(t, strings.Index(recorder.Body.String(), "response.output_text.delta"), strings.Index(recorder.Body.String(), "response.completed"))
			} else {
				assert.JSONEq(t, body, recorder.Body.String())
			}
		})
	}
}

func TestExistingRoutesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name              string
		mode              int
		format            types.RelayFormat
		requestPath, want string
	}{
		{"chat", relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, "", "/v1/chat/completions"},
		{"fim", relayconstant.RelayModeCompletions, types.RelayFormatOpenAI, "", "/beta/completions"},
		{"claude", relayconstant.RelayModeChatCompletions, types.RelayFormatClaude, "", "/anthropic/v1/messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://upstream.example"}, RelayMode: tc.mode, RelayFormat: tc.format, RequestURLPath: tc.requestPath}
			got, err := (&Adaptor{}).GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, "https://upstream.example"+tc.want, got)
		})
	}
}
