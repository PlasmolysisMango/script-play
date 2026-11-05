// Package llm abstracts model providers.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Message is one chat turn. Agent prompts are assembled by the engine and sent
// as a single user message.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Image is an attachment an agent may look at (character portraits, evidence
// photos). Text files are not supported: the provider only receives the prompt.
type Image struct {
	Path string
	MIME string
}

// Request is one generation request.
type Request struct {
	SeatID      string
	SpeakerName string
	RoomID      string
	GameID      string
	PhaseID     string
	Provider    string
	Model       string
	System      string
	Messages    []Message
	Images      []Image
	MaxTokens   int
	Temperature float64
	// JSON asks the provider to return a single JSON object.
	JSON bool
}

// Provider is a streaming text generator.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request, sink func(delta string) error) error
}

// ErrNoProvider is returned when the requested provider is not registered.
var ErrNoProvider = errors.New("llm provider not registered")

// Registry holds providers and enforces a process-wide concurrency cap so a
// 2 vCPU box is not flooded with CLI subprocesses.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	def       string
	sem       chan struct{}
}

// NewRegistry returns a registry with the given default provider.
func NewRegistry(def string, maxConcurrent int) *Registry {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Registry{
		providers: map[string]Provider{},
		def:       def,
		sem:       make(chan struct{}, maxConcurrent),
	}
}

// Register adds a provider.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Names lists registered providers.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Default returns the default provider name.
func (r *Registry) Default() string { return r.def }

// Resolve picks the provider for a request.
func (r *Registry) Resolve(name string) (Provider, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" || name == "default" {
		name = r.def
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, name, fmt.Errorf("%w: %s", ErrNoProvider, name)
	}
	return p, name, nil
}

// Complete runs a request to completion, returning the accumulated text.
// sink (optional) receives deltas.
func (r *Registry) Complete(ctx context.Context, req Request, sink func(string) error) (string, string, error) {
	p, name, err := r.Resolve(req.Provider)
	if err != nil {
		return "", name, err
	}
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return "", name, ctx.Err()
	}
	defer func() { <-r.sem }()

	var b strings.Builder
	var mu sync.Mutex
	wrap := func(delta string) error {
		mu.Lock()
		b.WriteString(delta)
		mu.Unlock()
		if sink != nil {
			return sink(delta)
		}
		return nil
	}
	if err := p.Stream(ctx, req, wrap); err != nil {
		// Keep partial output available for logging but surface the error.
		return b.String(), name, err
	}
	return b.String(), name, nil
}
