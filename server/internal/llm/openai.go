package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenAI talks to any OpenAI-compatible endpoint (DeepSeek, Qwen, Ollama,
// one-api, ...). It also carries the multimodal path used by image imports.
type OpenAI struct {
	BaseURL     string
	APIKey      string
	Model       string
	VisionModel string
	Client      *http.Client
}

// NewOpenAI builds a provider. BaseURL must include the version suffix, e.g.
// https://api.deepseek.com/v1.
func NewOpenAI(baseURL, apiKey, model, visionModel string, timeout time.Duration) *OpenAI {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &OpenAI{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIKey:      apiKey,
		Model:       model,
		VisionModel: visionModel,
		Client:      &http.Client{Timeout: timeout},
	}
}

// Name implements Provider.
func (o *OpenAI) Name() string { return "openai" }

type openAIContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openAIRequest struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Stream         bool            `json:"stream"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat map[string]any  `json:"response_format,omitempty"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream implements Provider.
func (o *OpenAI) Stream(ctx context.Context, req Request, sink func(string) error) error {
	if o.BaseURL == "" {
		return fmt.Errorf("openai provider: SP_OPENAI_BASE_URL is empty")
	}
	model := req.Model
	if model == "" {
		model = o.Model
	}
	if len(req.Images) > 0 && o.VisionModel != "" {
		model = o.VisionModel
	}

	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: req.System})
	}
	for i, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		if i == len(req.Messages)-1 && len(req.Images) > 0 {
			parts, err := buildParts(m.Text, req.Images)
			if err != nil {
				return err
			}
			msgs = append(msgs, openAIMessage{Role: role, Content: parts})
			continue
		}
		msgs = append(msgs, openAIMessage{Role: role, Content: m.Text})
	}

	body := openAIRequest{
		Model:       model,
		Messages:    msgs,
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSON {
		body.ResponseFormat = map[string]any{"type": "json_object"}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.Client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("openai provider: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		var ch openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		if ch.Error != nil {
			return fmt.Errorf("openai provider: %s", ch.Error.Message)
		}
		for _, c := range ch.Choices {
			if c.Delta.Content == "" {
				continue
			}
			if sink != nil {
				if err := sink(c.Delta.Content); err != nil {
					return err
				}
			}
		}
	}
	return scanner.Err()
}

func buildParts(text string, images []Image) ([]openAIContent, error) {
	parts := make([]openAIContent, 0, len(images)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, openAIContent{Type: "text", Text: text})
	}
	for _, img := range images {
		b, err := os.ReadFile(img.Path)
		if err != nil {
			return nil, fmt.Errorf("read image %s: %w", img.Path, err)
		}
		mime := img.MIME
		if mime == "" {
			mime = guessMIME(img.Path)
		}
		url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
		parts = append(parts, openAIContent{Type: "image_url", ImageURL: &openAIImageURL{URL: url}})
	}
	return parts, nil
}

func guessMIME(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
