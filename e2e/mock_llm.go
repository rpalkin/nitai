//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
)

// LLMRequest records a received chat completion request.
type LLMRequest struct {
	Body []byte
}

// LLMMock is a configurable mock OpenAI-compatible LLM server.
type LLMMock struct {
	Server *httptest.Server

	mu                sync.Mutex
	requests          []LLMRequest
	embeddingRequests []LLMRequest

	// Default response returned for all chat completion requests.
	DefaultResponse json.RawMessage

	// Optional: custom handler that inspects request body and returns
	// a per-request response. If nil, DefaultResponse is used.
	ResponseFunc func(reqBody []byte) (statusCode int, response json.RawMessage)

	// Optional: custom handler for embedding requests. If nil, returns zero vectors.
	EmbeddingResponseFunc func(reqBody []byte) (statusCode int, response json.RawMessage)
}

var defaultLLMResponse = json.RawMessage(`{
    "id": "chatcmpl-test-1",
    "object": "chat.completion",
    "model": "test-model",
    "choices": [{
        "index": 0,
        "message": {
            "role": "assistant",
            "tool_calls": [{
                "id": "call_1",
                "type": "function",
                "function": {
                    "name": "final_result",
                    "arguments": "{\"summary\":\"The PR adds order processing but has a potential nil pointer issue and missing error propagation.\",\"comments\":[{\"file_path\":\"src/handler.go\",\"line_start\":12,\"line_end\":12,\"body\":\"CalculateTotal may return nil if Items is empty. Add a length check before calling.\"},{\"file_path\":\"src/handler.go\",\"line_start\":17,\"line_end\":17,\"body\":\"This silently swallows the result. Consider returning it or logging the error.\"}]}"
                }
            }]
        },
        "finish_reason": "stop"
    }],
    "usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
}`)

func NewLLMMock() *LLMMock {
	l := &LLMMock{
		DefaultResponse: defaultLLMResponse,
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		panic(err)
	}
	l.Server = httptest.NewUnstartedServer(http.HandlerFunc(l.handle))
	l.Server.Listener = ln
	l.Server.Start()
	return l
}

func (l *LLMMock) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/v1/chat/completions":
		l.mu.Lock()
		l.requests = append(l.requests, LLMRequest{Body: bodyBytes})
		responseFunc := l.ResponseFunc
		defaultResp := l.DefaultResponse
		l.mu.Unlock()

		if responseFunc != nil {
			statusCode, resp := responseFunc(bodyBytes)
			w.WriteHeader(statusCode)
			w.Write(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(defaultResp)

	case "/v1/embeddings":
		l.mu.Lock()
		l.embeddingRequests = append(l.embeddingRequests, LLMRequest{Body: bodyBytes})
		embeddingFunc := l.EmbeddingResponseFunc
		l.mu.Unlock()

		if embeddingFunc != nil {
			statusCode, resp := embeddingFunc(bodyBytes)
			w.WriteHeader(statusCode)
			w.Write(resp)
			return
		}
		// Default: return zero vectors of dimension 1536 (text-embedding-3-small)
		resp := l.buildDefaultEmbeddingResponse(bodyBytes)
		w.WriteHeader(http.StatusOK)
		w.Write(resp)

	default:
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

// buildDefaultEmbeddingResponse returns a valid embeddings response with zero vectors.
// It parses the input to return the correct number of embeddings.
func (l *LLMMock) buildDefaultEmbeddingResponse(bodyBytes []byte) []byte {
	var req struct {
		Input any    `json:"input"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal(bodyBytes, &req)

	// Count inputs: may be a string or array of strings
	count := 1
	switch v := req.Input.(type) {
	case []any:
		count = len(v)
	}

	const dim = 1536
	zeroVec := make([]float64, dim)

	type embeddingItem struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	}
	data := make([]embeddingItem, count)
	for i := range data {
		data[i] = embeddingItem{Object: "embedding", Index: i, Embedding: zeroVec}
	}

	resp := map[string]any{
		"object": "list",
		"data":   data,
		"model":  req.Model,
		"usage":  map[string]int{"prompt_tokens": count * 10, "total_tokens": count * 10},
	}
	b, _ := json.Marshal(resp)
	return b
}

func (l *LLMMock) Requests() []LLMRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LLMRequest, len(l.requests))
	copy(out, l.requests)
	return out
}

func (l *LLMMock) RequestCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.requests)
}

func (l *LLMMock) EmbeddingRequestCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.embeddingRequests)
}

func (l *LLMMock) SetEmbeddingResponseFunc(f func(reqBody []byte) (int, json.RawMessage)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.EmbeddingResponseFunc = f
}

func (l *LLMMock) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = nil
	l.embeddingRequests = nil
	l.ResponseFunc = nil
	l.EmbeddingResponseFunc = nil
}
