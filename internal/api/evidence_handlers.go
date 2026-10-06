package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *Server) listEvidenceChains(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	if _, err := s.services.Span(spanID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	generation, err := parseGenerationParam(request.URL.Query().Get("generation"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	chains := s.services.Evidence.List(spanID)
	if generation != 0 {
		filtered := chains[:0]
		for _, chain := range chains {
			if chain.Generation == generation {
				filtered = append(filtered, chain)
			}
		}
		chains = filtered
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": chains})
}

func (s *Server) getEvidenceChain(writer http.ResponseWriter, request *http.Request) {
	chain, exists := s.services.Evidence.Get(chi.URLParam(request, "id"))
	if !exists {
		writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: "evidence chain not found"})
		return
	}
	writeJSON(writer, http.StatusOK, chain)
}

// retrieveEvidence serves both incident handling and repair acceptance: the
// raw chains of one span inside a time range, sliced by topology generation.
func (s *Server) retrieveEvidence(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	if _, err := s.services.Span(spanID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	query := request.URL.Query()
	from, err := parseTimeParam(query.Get("from"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	to, err := parseTimeParam(query.Get("to"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		writeJSON(writer, http.StatusBadRequest, ErrorResponse{Error: "from must not be after to"})
		return
	}
	generation, err := parseGenerationParam(query.Get("generation"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	purpose := query.Get("purpose")
	if purpose != "" && purpose != "incident" && purpose != "acceptance" {
		writeJSON(writer, http.StatusBadRequest, ErrorResponse{Error: "purpose must be incident or acceptance"})
		return
	}
	bundle := s.services.Evidence.Query(spanID, from, to, generation)
	if purpose != "" {
		audit := map[string]any{
			"span_id": spanID, "purpose": purpose, "from": bundle.From, "to": bundle.To,
			"generation": generation, "chains": len(bundle.Generations),
		}
		if err := s.services.Persist("evidence-retrieval", spanID, audit); err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, bundle)
}

func parseTimeParam(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func parseGenerationParam(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}
