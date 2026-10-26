package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"void2md/dbc/pkg/config"
	"void2md/dbc/pkg/llm"
)

// LLMConfigRequest is the JSON body for updating the LLM configuration.
// Partial update: only non-empty fields are applied.
type LLMConfigRequest struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

// LLMConfigResponse is the JSON representation of the current LLM
// configuration. The API key is always masked.
type LLMConfigResponse struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	Message  string `json:"message,omitempty"`
}

// HandleLLMConfig handles the /config/llm endpoint:
//   - GET: return the current in-memory LLM configuration (api_key masked)
//   - PUT: update the in-memory LLM configuration (partial update with
//     non-empty fields) and rebuild the LLM client
//
// Changes are in-memory only; they are not persisted to the config file
// and are lost on restart (or restored via /config/llm/reset).
func (s *Service) HandleLLMConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		cur := s.llmCfg
		s.mu.RUnlock()
		writeLLMConfigResponse(w, http.StatusOK, cur, "")

	case http.MethodPut:
		var req LLMConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		updated := s.llmCfg
		if v := strings.TrimSpace(req.Endpoint); v != "" {
			updated.Endpoint = v
		}
		if v := strings.TrimSpace(req.Model); v != "" {
			updated.Model = v
		}
		if v := strings.TrimSpace(req.APIKey); v != "" {
			updated.APIKey = v
		}
		if updated.Endpoint == "" || updated.Model == "" || updated.APIKey == "" {
			s.mu.Unlock()
			http.Error(w, "endpoint, model and api_key must all be non-empty", http.StatusBadRequest)
			return
		}
		s.llmCfg = updated
		s.llmClient = llm.NewClient(updated.Endpoint, updated.Model, updated.APIKey)
		s.mu.Unlock()

		s.logger.Infof("LLM config updated: endpoint=%s model=%s", updated.Endpoint, updated.Model)
		writeLLMConfigResponse(w, http.StatusOK, updated, "LLM config updated")

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleResetLLMConfig handles the /config/llm/reset endpoint (POST):
// restore the LLM configuration to the values loaded from the config
// file at startup.
func (s *Service) HandleResetLLMConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	s.llmCfg = s.defaultLLMCfg
	s.llmClient = llm.NewClient(s.defaultLLMCfg.Endpoint, s.defaultLLMCfg.Model, s.defaultLLMCfg.APIKey)
	cur := s.llmCfg
	s.mu.Unlock()

	s.logger.Info("LLM config reset to startup values")
	writeLLMConfigResponse(w, http.StatusOK, cur, "LLM config reset to startup values")
}

func writeLLMConfigResponse(w http.ResponseWriter, code int, cfg config.LLMConfig, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(LLMConfigResponse{
		Endpoint: cfg.Endpoint,
		Model:    cfg.Model,
		APIKey:   maskAPIKey(cfg.APIKey),
		Message:  message,
	})
}

// maskAPIKey keeps only the last 4 characters visible.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
