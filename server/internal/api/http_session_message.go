package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"mindfs/server/internal/api/usecase"
	"mindfs/server/internal/session"
)

func (h *HTTPHandler) handleSessionUserMessage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RootID  string `json:"root_id"`
		Message string `json:"message"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest(err.Error()))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		respondError(w, http.StatusBadRequest, errInvalidRequest("one JSON object required"))
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	input.RootID = strings.TrimSpace(input.RootID)
	if input.RootID == "" || key == "" || strings.TrimSpace(input.Message) == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root_id, session key and message required"))
		return
	}
	current, err := h.service().GetSession(r.Context(), usecase.GetSessionInput{RootID: input.RootID, Key: key})
	if err != nil || current == nil {
		respondError(w, http.StatusNotFound, errInvalidRequest("session not found"))
		return
	}
	agentName := session.InferAgentFromSession(current)
	if current.Type != session.TypeChat || current.ClosedAt != nil || agentName == "" {
		respondError(w, http.StatusConflict, errInvalidRequest("an open chat session with an agent is required"))
		return
	}
	ws := &WSHandler{AppContext: h.AppContext}
	planRequested, content := parsePlanMessage(input.Message)
	if strings.TrimSpace(content) == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("message required"))
		return
	}
	if planRequested && !current.PlanMode {
		manager, err := h.AppContext.GetSessionManager(input.RootID)
		if err == nil {
			err = manager.UpdatePlanMode(r.Context(), current, true)
		}
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		current.PlanMode = true
		ws.switchSessionRuntimePlanMode(r.Context(), key, current, true)
		ws.broadcastSessionMetaUpdated(input.RootID, current)
	}
	now := time.Now().UTC()
	requestID := "cli-" + now.Format("20060102150405.000000000")
	queued := ws.submitSessionMessage(sessionMessageJob{
		RootID: input.RootID, Key: key, RequestID: requestID,
		RuntimeRootPath: sessionRuntimeRootPath(current),
		SessionType:     current.Type, SessionName: current.Name,
		ClientCtx: parseClientContext(nil, input.RootID),
		User: PendingUserMessage{
			Agent: agentName, Model: current.Model,
			Mode: session.InferModeFromSession(current), Effort: session.InferEffortFromSession(current),
			FastService: session.InferFastServiceFromSession(current), PlanMode: current.PlanMode,
			Content: content, Timestamp: now,
		},
	})
	respondJSON(w, http.StatusAccepted, map[string]any{
		"root_id": input.RootID, "session_key": key, "request_id": requestID, "queued": queued,
	})
}
