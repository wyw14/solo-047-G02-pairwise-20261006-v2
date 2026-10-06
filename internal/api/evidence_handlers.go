package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// spanEvidence returns a span's evidence chains inside a time window. Each
// topology generation is returned as its own chain; the optional generation
// query pin restricts the result to one generation.
func (s *Server) spanEvidence(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	if _, err := s.services.Span(spanID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	from, to, generation, ok := parseEvidenceQuery(writer, request)
	if !ok {
		return
	}
	chains := s.services.Evidence.ForSpan(spanID, from, to, generation)
	writeJSON(writer, http.StatusOK, map[string]any{"span_id": spanID, "generations": chains})
}

// incidentEvidence retrieves the raw evidence for one incident. The incident
// is bound to the generation it was raised against, so scans from later
// post-repair generations can never enter the incident's bundle.
func (s *Server) incidentEvidence(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	incidentID := chi.URLParam(request, "incidentID")
	from, to, _, ok := parseEvidenceQuery(writer, request)
	if !ok {
		return
	}
	bundle, err := s.services.Evidence.ForIncident(incidentID, from, to)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if bundle.SpanID != spanID {
		writeJSON(writer, http.StatusNotFound, ErrorResponse{Error: "incident is not bound to this span"})
		return
	}
	writeJSON(writer, http.StatusOK, bundle)
}

// repairEvidence serves repair acceptance. It defaults to the span's current
// topology generation so verification is always performed against the
// generation actually in service — never an older repaired-away generation.
func (s *Server) repairEvidence(writer http.ResponseWriter, request *http.Request) {
	spanID := chi.URLParam(request, "id")
	if _, err := s.services.Span(spanID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	from, to, generation, ok := parseEvidenceQuery(writer, request)
	if !ok {
		return
	}
	if generation == 0 {
		snapshot, err := s.services.Topology.Snapshot(spanID)
		if err != nil {
			writeError(writer, http.StatusNotFound, err)
			return
		}
		generation = snapshot.Generation
	}
	chains := s.services.Evidence.ForSpan(spanID, from, to, generation)
	writeJSON(writer, http.StatusOK, map[string]any{
		"span_id": spanID, "current_generation": generation, "generations": chains,
	})
}

func parseEvidenceQuery(writer http.ResponseWriter, request *http.Request) (time.Time, time.Time, uint64, bool) {
	query := request.URL.Query()
	var from, to time.Time
	var err error
	if raw := query.Get("from"); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, &pageValueError{value: raw})
			return time.Time{}, time.Time{}, 0, false
		}
	}
	if raw := query.Get("to"); raw != "" {
		to, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, &pageValueError{value: raw})
			return time.Time{}, time.Time{}, 0, false
		}
	}
	var generation uint64
	if raw := query.Get("generation"); raw != "" {
		value, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil || value == 0 {
			writeError(writer, http.StatusBadRequest, &pageValueError{value: raw})
			return time.Time{}, time.Time{}, 0, false
		}
		generation = value
	}
	return from.UTC(), to.UTC(), generation, true
}
