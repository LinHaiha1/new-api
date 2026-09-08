package zhipu_4v

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
	for _, tc := range []struct{ base, want string }{
		{"https://custom.example", "https://custom.example/api/v1/responses"},
		{"", "https://open.bigmodel.cn/api/v1/responses"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			got, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: tc.base}, RelayMode: relayconstant.RelayModeResponses})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
func TestResponsesPassthrough(t *testing.T) {
	var req dto.OpenAIResponsesRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"glm-5","input":"hello","temperature":0,"top_p":0,"stream":false,"store":false,"tools":[{"type":"function","name":"lookup"}],"reasoning":{"effort":"high"}}`, &req))
	got, err := (&Adaptor{}).ConvertOpenAIResponsesRequest(nil, nil, req)
	require.NoError(t, err)
	assert.Equal(t, req, got)
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
		{"chat", relayconstant.RelayModeChatCompletions, types.RelayFormatOpenAI, "", "/api/paas/v4/chat/completions"},
		{"embedding", relayconstant.RelayModeEmbeddings, types.RelayFormatOpenAI, "", "/api/paas/v4/embeddings"},
		{"images", relayconstant.RelayModeImagesGenerations, types.RelayFormatOpenAI, "", "/api/paas/v4/images/generations"},
		{"claude", relayconstant.RelayModeChatCompletions, types.RelayFormatClaude, "", "/api/anthropic/v1/messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://upstream.example"}, RelayMode: tc.mode, RelayFormat: tc.format, RequestURLPath: tc.requestPath}
			got, err := (&Adaptor{}).GetRequestURL(info)
			require.NoError(t, err)
			assert.Equal(t, "https://upstream.example"+tc.want, got)
		})
	}
}
