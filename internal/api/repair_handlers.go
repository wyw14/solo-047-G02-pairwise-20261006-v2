package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-103/internal/otdr"
)

func (s *Server) addAcceptanceProof(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	var input AcceptanceRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.AcceptanceID == "" {
		input.AcceptanceID = s.services.IDs.New("acceptance")
	}
	proof := otdr.Proof{
		ID: s.services.IDs.New("proof"), Direction: input.Direction, Passed: input.Passed,
		LossDB: input.LossDB, Message: input.Message, MeasuredAt: s.services.Clock.Now().UTC(),
	}
	// Anchor the directional proof to the generation currently in service
	// before the acceptance decision, so a failing proof is retained as
	// evidence too. Proofs from other generations live in other chains and can
	// never complete this one.
	snapshot, topologyErr := s.services.Topology.Snapshot(spanID)
	if topologyErr != nil {
		writeError(writer, http.StatusConflict, topologyErr)
		return
	}
	chain, evidenceErr := s.services.Evidence.AddProof(spanID, snapshot.Generation, proof)
	if evidenceErr != nil {
		writeError(writer, http.StatusConflict, evidenceErr)
		return
	}
	result, err := s.services.Acceptance.AddProof(request.Context(), input.AcceptanceID, spanID, proof)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if err := s.services.Persist("repair-acceptance", input.AcceptanceID, result); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusAccepted
	if result.Restored {
		status = http.StatusOK
	}
	writeJSON(writer, status, map[string]any{"acceptance": result, "evidence_chain": chain})
}

func (s *Server) getAcceptance(writer http.ResponseWriter, request *http.Request) {
	result, exists := s.services.Acceptance.Get(chi.URLParam(request, "id"))
	if !exists {
		writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: "acceptance not found"})
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
