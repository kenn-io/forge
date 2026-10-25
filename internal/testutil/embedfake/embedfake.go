// Package embedfake serves an OpenAI-compatible embeddings endpoint for
// tests. A text's vector counts how often each topic word appears, plus a
// constant so no vector is zero, which makes nearest neighbors predictable.
package embedfake

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.kenn.io/kit/embedconfig"
)

// Server is a running fake endpoint.
type Server struct {
	topics []string
	url    string

	mu          sync.Mutex
	inputs      []string
	model       string
	reject      string
	refuseModel string
}

// New starts a fake endpoint for t whose vector axes are topics.
func New(t testing.TB, topics ...string) *Server {
	t.Helper()
	s := &Server{topics: slices.Clone(topics)}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(server.Close)
	s.url = server.URL + "/v1"
	return s
}

// Embedder returns an [embeddings] configuration that uses this endpoint.
func (s *Server) Embedder(model string) embedconfig.Embedder {
	return embedconfig.Embedder{BaseURL: s.url, Model: model, Dims: len(s.topics) + 1}
}

// Inputs returns every text the endpoint has encoded since the last reset.
func (s *Server) Inputs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inputs)
}

// ResetInputs forgets the texts encoded so far.
func (s *Server) ResetInputs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = nil
}

// LastModel is the model named by the most recent request.
func (s *Server) LastModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

// RejectInputsContaining answers 400 "input too long" for any request that
// includes text containing marker. An empty marker rejects nothing.
func (s *Server) RejectInputsContaining(marker string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject = marker
}

// RefuseModel answers 401 for every request naming model. An empty model
// refuses nothing.
func (s *Server) RefuseModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseModel = model
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.inputs = append(s.inputs, req.Input...)
	s.model = req.Model
	reject, refused := s.reject, s.refuseModel != "" && s.refuseModel == req.Model
	s.mu.Unlock()
	if refused {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.MarshalWrite(w, map[string]any{"error": map[string]any{"message": "invalid api key"}})
		return
	}
	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	data := make([]item, len(req.Input))
	for i, text := range req.Input {
		if reject != "" && strings.Contains(text, reject) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.MarshalWrite(w, map[string]any{"error": map[string]any{
				"code": "context_length_exceeded", "message": "input is too long",
			}})
			return
		}
		data[i] = item{Index: i, Embedding: s.vector(text)}
	}
	_ = json.MarshalWrite(w, map[string]any{"data": data})
}

func (s *Server) vector(text string) []float32 {
	lower := strings.ToLower(text)
	vec := make([]float32, len(s.topics)+1)
	for i, topic := range s.topics {
		vec[i] = float32(strings.Count(lower, topic))
	}
	vec[len(s.topics)] = 0.1
	return vec
}
