package ollama

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
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
	for _, tc := range []struct {
		mode int
		path string
	}{
		{relayconstant.RelayModeResponses, "/v1/responses"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "http://localhost:11434"}, RelayMode: tc.mode})
			require.NoError(t, err)
			assert.Equal(t, "http://localhost:11434"+tc.path, got)
		})
	}
}
func TestResponsesConversion(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	var req dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"qwen3","input":"hello","temperature":0,"stream":false,"store":false,"reasoning":{"effort":"high","summary":"auto"},"tools":[{"type":"function","name":"lookup"}]}`, &req))
	got, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, info, req)
	require.NoError(t, err)
	assert.Equal(t, req, got)
	assert.Equal(t, "high", info.ReasoningEffort)
	payload, err := common.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"temperature":0`)
	assert.Contains(t, string(payload), `"stream":false`)
	assert.Contains(t, string(payload), `"store":false`)
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
		{"chat", relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, "", "/api/chat"},
		{"embedding", relayconstant.RelayModeEmbeddings, types.RelayFormatOpenAI, "", "/api/embed"},
		{"generate", relayconstant.RelayModeCompletions, types.RelayFormatOpenAI, "", "/api/generate"},
		{"legacy_path", relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, "/v1/completions", "/api/generate"},
		{"claude_still_native_chat", relayconstant.RelayModeChatCompletions, types.RelayFormatClaude, "", "/api/chat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://upstream.example"}, RelayMode: tc.mode, RelayFormat: tc.format, RequestURLPath: tc.requestPath}
			got, err := (&Adaptor{}).GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, "https://upstream.example"+tc.want, got)
		})
	}
}
