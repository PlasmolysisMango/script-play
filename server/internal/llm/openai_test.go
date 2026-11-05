package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The OpenAI-compatible provider is the escape hatch for DeepSeek / Qwen /
// Ollama / one-api. A fake SSE endpoint proves the request shape (system
// prompt, stream flag, JSON mode, image parts) and the delta parsing.
func TestOpenAIProviderStreamsAndSendsImages(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		ResponseFormat map[string]any `json:"response_format"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range []string{"我", "在", "塔下"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", piece)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	imgPath := filepath.Join(t.TempDir(), "page.png")
	if err := os.WriteFile(imgPath, []byte("fakepng"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewOpenAI(srv.URL+"/v1", "test-key", "deepseek-chat", "qwen-vl-max", 5*time.Second)
	var deltas []string
	err := p.Stream(context.Background(), Request{
		System:   "你是主持人",
		Messages: []Message{{Role: "user", Text: "读这页剧本"}},
		Images:   []Image{{Path: imgPath, MIME: "image/png"}},
		JSON:     true,
	}, func(d string) error {
		deltas = append(deltas, d)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(deltas, "") != "我在塔下" {
		t.Fatalf("deltas = %v", deltas)
	}
	if !got.Stream || got.Model != "qwen-vl-max" {
		t.Errorf("stream=%v model=%q (vision model must win when images are attached)", got.Stream, got.Model)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v", got.Messages)
	}
	if got.ResponseFormat["type"] != "json_object" {
		t.Errorf("response_format = %v", got.ResponseFormat)
	}
	var parts []struct {
		Type     string `json:"type"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(got.Messages[1].Content, &parts); err != nil {
		t.Fatalf("content is not a multimodal parts array: %v (%s)", err, got.Messages[1].Content)
	}
	if len(parts) != 2 || parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image part missing: %+v", parts)
	}
}

func TestOpenAIProviderSurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI(srv.URL+"/v1", "", "m", "", 5*time.Second)
	err := p.Stream(context.Background(), Request{Messages: []Message{{Role: "user", Text: "hi"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
