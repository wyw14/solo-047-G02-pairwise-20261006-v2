package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wyw14/cry-103/internal/evidence"
	"github.com/wyw14/cry-103/internal/otdr"
)

func TestEvidenceChainHTTPFlow(t *testing.T) {
	server := testServer(t)

	body, _ := json.Marshal(ScanRequest{StationID: "east", LengthKM: 126, Chunks: 4})
	scan := httptest.NewRecorder()
	server.Handler().ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/spans/S3/otdr-scans", bytes.NewReader(body)))
	if scan.Code != http.StatusCreated {
		t.Fatalf("scan status=%d body=%s", scan.Code, scan.Body.String())
	}

	list := httptest.NewRecorder()
	server.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/spans/S3/evidence-chains", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var listed struct {
		Items []evidence.Chain `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("expected one evidence chain, got %+v", listed.Items)
	}
	chain := listed.Items[0]
	if chain.Generation != 1 || chain.TraceID == "" || len(chain.Segments) != 3 {
		t.Fatalf("chain must link trace, generation, and segment curve: %+v", chain)
	}
	if chain.Sufficient {
		t.Fatal("chain without directional proofs must be insufficient")
	}
	insufficient := 0
	for _, finding := range chain.Findings {
		if finding.Kind == evidence.FindingInsufficient {
			insufficient++
		}
	}
	if insufficient != 2 {
		t.Fatalf("expected two missing-direction findings, got %+v", chain.Findings)
	}

	single := httptest.NewRecorder()
	server.Handler().ServeHTTP(single, httptest.NewRequest(http.MethodGet, "/api/evidence-chains/"+chain.ID, nil))
	if single.Code != http.StatusOK {
		t.Fatalf("get chain status=%d body=%s", single.Code, single.Body.String())
	}

	bundle := httptest.NewRecorder()
	server.Handler().ServeHTTP(bundle, httptest.NewRequest(http.MethodGet,
		"/api/spans/S3/evidence?purpose=incident&from=2026-08-24T02:00:00Z&to=2026-08-24T04:00:00Z", nil))
	if bundle.Code != http.StatusOK {
		t.Fatalf("bundle status=%d body=%s", bundle.Code, bundle.Body.String())
	}
	var retrieved evidence.Bundle
	if err := json.Unmarshal(bundle.Body.Bytes(), &retrieved); err != nil {
		t.Fatal(err)
	}
	if len(retrieved.Generations) != 1 || retrieved.Generations[0].Generation != 1 || len(retrieved.Generations[0].Chains) != 1 {
		t.Fatalf("bundle must hold the generation-1 chain, got %+v", retrieved.Generations)
	}

	outside := httptest.NewRecorder()
	server.Handler().ServeHTTP(outside, httptest.NewRequest(http.MethodGet,
		"/api/spans/S3/evidence?from=2026-08-24T04:00:00Z&to=2026-08-24T05:00:00Z", nil))
	var empty evidence.Bundle
	if err := json.Unmarshal(outside.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Generations) != 0 {
		t.Fatalf("window outside the scan must be empty, got %+v", empty.Generations)
	}

	inverted := httptest.NewRecorder()
	server.Handler().ServeHTTP(inverted, httptest.NewRequest(http.MethodGet,
		"/api/spans/S3/evidence?from=2026-08-24T04:00:00Z&to=2026-08-24T02:00:00Z", nil))
	if inverted.Code != http.StatusBadRequest {
		t.Fatalf("inverted window status=%d body=%s", inverted.Code, inverted.Body.String())
	}

	target := server.services.Spans["S3"]
	now := server.services.Clock.Now()
	if _, err := target.MarkCut(now); err != nil {
		t.Fatal(err)
	}
	if _, err := target.StartRepair(now); err != nil {
		t.Fatal(err)
	}
	if _, err := target.BeginProof(now); err != nil {
		t.Fatal(err)
	}

	for _, direction := range []otdr.Direction{otdr.DirectionEastToWest, otdr.DirectionWestToEast} {
		proofBody, _ := json.Marshal(AcceptanceRequest{AcceptanceID: "acc-1", Direction: direction, Passed: true, LossDB: 0.4})
		proof := httptest.NewRecorder()
		server.Handler().ServeHTTP(proof, httptest.NewRequest(http.MethodPost, "/api/repairs/S3/acceptance", bytes.NewReader(proofBody)))
		if proof.Code != http.StatusAccepted && proof.Code != http.StatusOK {
			t.Fatalf("proof %s status=%d body=%s", direction, proof.Code, proof.Body.String())
		}
	}

	after := httptest.NewRecorder()
	server.Handler().ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/api/evidence-chains/"+chain.ID, nil))
	var linked evidence.Chain
	if err := json.Unmarshal(after.Body.Bytes(), &linked); err != nil {
		t.Fatal(err)
	}
	if len(linked.Proofs) != 2 || !linked.Sufficient {
		t.Fatalf("chain must carry both directional proofs and turn sufficient, got %+v", linked)
	}
	if linked.Generation != 1 {
		t.Fatalf("proofs must land on the measured generation 1, got %d", linked.Generation)
	}
}
