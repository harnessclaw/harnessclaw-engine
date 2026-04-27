package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
	"harnessclaw-go/internal/agent"
	"harnessclaw-go/internal/tool"
)

// AgentHandler handles agent definition CRUD endpoints.
type AgentHandler struct {
	service *agent.AgentService
	logger  *zap.Logger
}

// NewAgentHandler creates a new agent handler.
func NewAgentHandler(service *agent.AgentService, logger *zap.Logger) *AgentHandler {
	return &AgentHandler{service: service, logger: logger}
}

// RegisterRoutes registers agent management routes on the mux.
func (h *AgentHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /console/v1/agents", h.Create)
	mux.HandleFunc("GET /console/v1/agents", h.List)
	mux.HandleFunc("GET /console/v1/agents/{name}", h.Get)
	mux.HandleFunc("PUT /console/v1/agents/{name}", h.Update)
	mux.HandleFunc("DELETE /console/v1/agents/{name}", h.Delete)
	mux.HandleFunc("POST /console/v1/agents/import", h.Import)
	mux.HandleFunc("GET /console/v1/agents/{name}/export", h.Export)
}

// createRequest is the JSON body for POST /console/v1/agents.
type createRequest struct {
	Name            string              `json:"name"`
	DisplayName     string              `json:"display_name"`
	Description     string              `json:"description"`
	SystemPrompt    string              `json:"system_prompt"`
	AgentType       string              `json:"agent_type"`
	Profile         string              `json:"profile"`
	Model           string              `json:"model"`
	MaxTurns        int                 `json:"max_turns"`
	Tools           []string            `json:"tools"`
	AllowedTools    []string            `json:"allowed_tools"`
	DisallowedTools []string            `json:"disallowed_tools"`
	Skills          []string            `json:"skills"`
	AutoTeam        bool                `json:"auto_team"`
	SubAgents       []agent.SubAgentDef `json:"sub_agents"`
}

// Create handles POST /console/v1/agents
func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "name is required")
		return
	}

	def := &agent.AgentDefinition{
		Name:            req.Name,
		DisplayName:     req.DisplayName,
		Description:     req.Description,
		SystemPrompt:    req.SystemPrompt,
		AgentType:       parseAgentType(req.AgentType),
		Profile:         req.Profile,
		Model:           req.Model,
		MaxTurns:        req.MaxTurns,
		Tools:           req.Tools,
		AllowedTools:    req.AllowedTools,
		DisallowedTools: req.DisallowedTools,
		Skills:          req.Skills,
		AutoTeam:        req.AutoTeam,
		SubAgents:       req.SubAgents,
		Source:          "custom",
	}

	result, err := h.service.Create(r.Context(), def)
	if err != nil {
		writeError(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	writeSuccess(w, http.StatusCreated, result)
}

// List handles GET /console/v1/agents
func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	filter := &agent.AgentFilter{}
	if v := r.URL.Query().Get("agent_type"); v != "" {
		filter.AgentType = &v
	}
	if v := r.URL.Query().Get("source"); v != "" {
		filter.Source = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}

	defs, err := h.service.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeList(w, defs, len(defs))
}

// Get handles GET /console/v1/agents/{name}
func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	def, err := h.service.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeSuccess(w, http.StatusOK, def)
}

// Update handles PUT /console/v1/agents/{name}
func (h *AgentHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var updates agent.AgentUpdate
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid JSON: "+err.Error())
		return
	}

	result, err := h.service.Update(r.Context(), name, &updates)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeSuccess(w, http.StatusOK, result)
}

// Delete handles DELETE /console/v1/agents/{name}
func (h *AgentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.service.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// importRequest is the JSON body for POST /console/v1/agents/import.
type importRequest struct {
	Dir string `json:"dir"`
}

// Import handles POST /console/v1/agents/import
func (h *AgentHandler) Import(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "invalid JSON: "+err.Error())
		return
	}
	if req.Dir == "" {
		writeError(w, http.StatusBadRequest, "INVALID_INPUT", "dir is required")
		return
	}

	imported, errs := h.service.ImportFromYAML(r.Context(), req.Dir)
	errStrs := make([]string, len(errs))
	for i, e := range errs {
		errStrs[i] = e.Error()
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"imported": imported,
		"errors":   errStrs,
	})
}

// Export handles GET /console/v1/agents/{name}/export
func (h *AgentHandler) Export(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	def, err := h.service.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	// Convert to YAML-friendly struct (matching the existing yamlAgentDef format from loader.go)
	yamlDef := map[string]any{
		"name":        def.Name,
		"description": def.Description,
		"agent_type":  string(def.AgentType),
	}
	if def.DisplayName != "" {
		yamlDef["display_name"] = def.DisplayName
	}
	if def.SystemPrompt != "" {
		yamlDef["system_prompt"] = def.SystemPrompt
	}
	if def.Profile != "" {
		yamlDef["profile"] = def.Profile
	}
	if def.Model != "" {
		yamlDef["model"] = def.Model
	}
	if def.MaxTurns > 0 {
		yamlDef["max_turns"] = def.MaxTurns
	}
	if len(def.Tools) > 0 {
		yamlDef["tools"] = def.Tools
	}
	if len(def.AllowedTools) > 0 {
		yamlDef["allowed_tools"] = def.AllowedTools
	}
	if len(def.DisallowedTools) > 0 {
		yamlDef["disallowed_tools"] = def.DisallowedTools
	}
	if len(def.Skills) > 0 {
		yamlDef["skills"] = def.Skills
	}
	if def.AutoTeam {
		yamlDef["auto_team"] = true
		if len(def.SubAgents) > 0 {
			yamlDef["sub_agents"] = def.SubAgents
		}
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename="+name+".yaml")
	yaml.NewEncoder(w).Encode(yamlDef)
}

// parseAgentType converts a string to tool.AgentType.
// Duplicated from loader.go to avoid exporting internal parsing.
func parseAgentType(s string) tool.AgentType {
	switch s {
	case "sync":
		return tool.AgentTypeSync
	case "async":
		return tool.AgentTypeAsync
	case "teammate":
		return tool.AgentTypeTeammate
	case "coordinator":
		return tool.AgentTypeCoordinator
	case "custom":
		return tool.AgentTypeCustom
	default:
		return tool.AgentTypeSync
	}
}
