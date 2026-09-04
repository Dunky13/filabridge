package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const tagCapableOpenAPI = `{
	"paths": {
		"/spool": {"get": {"parameters": [{"name": "tag", "in": "query"}]}},
		"/spool/{spool_id}/tag": {"post": {}}
	}
}`

func TestDetectCapabilitiesFindsTagAPIOperationsFromOpenAPISchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/openapi.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tagCapableOpenAPI))
	}))
	defer server.Close()

	client := NewSpoolmanClient(server.URL, 5, "", "")
	capabilities, err := client.DetectCapabilities()
	if err != nil {
		t.Fatalf("DetectCapabilities() error = %v", err)
	}
	if !capabilities.TagUIDLookup {
		t.Fatal("DetectCapabilities().TagUIDLookup = false, want true")
	}
	if !capabilities.TagAssociation {
		t.Fatal("DetectCapabilities().TagAssociation = false, want true")
	}
}

func TestLookupSpoolByTagUIDReportsUnsupportedWithoutQueryingLegacySpoolman(t *testing.T) {
	spoolQueried := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"paths":{"/spool":{"get":{"parameters":[]}}}}`))
		case "/api/v1/spool":
			spoolQueried = true
			_, _ = w.Write([]byte(`[{"id": 1}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewSpoolmanClient(server.URL, 5, "", "")
	spool, err := client.LookupSpoolByTagUID("04A2B3C4")
	if !errors.Is(err, ErrSpoolmanTagAPIUnsupported) {
		t.Fatalf("LookupSpoolByTagUID() error = %v, want ErrSpoolmanTagAPIUnsupported", err)
	}
	if spool != nil {
		t.Fatalf("LookupSpoolByTagUID() spool = %+v, want nil", spool)
	}
	if spoolQueried {
		t.Fatal("legacy /spool endpoint queried with unsupported tag filter")
	}
}

func TestAssociateTagWithSpoolUsesV027TagEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagCapableOpenAPI))
		case "/api/v1/spool/42/tag":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			var body struct {
				UID    string `json:"uid"`
				Format string `json:"format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
				http.Error(w, "invalid test request", http.StatusBadRequest)
				return
			}
			if body.UID != "04:a2:b3:c4" || body.Format != "openprinttag" {
				t.Errorf("body = %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"uid":"04A2B3C4","format":"openprinttag","added":"2026-09-04T10:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewSpoolmanClient(server.URL, 5, "", "")
	tag, err := client.AssociateTagWithSpool(42, "04:a2:b3:c4", "openprinttag")
	if err != nil {
		t.Fatalf("AssociateTagWithSpool() error = %v", err)
	}
	if tag.UID != "04A2B3C4" || tag.Format != "openprinttag" {
		t.Fatalf("AssociateTagWithSpool() = %+v", tag)
	}
}

func TestLookupSpoolByTagUIDUsesExactTagQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/openapi.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(tagCapableOpenAPI))
		case "/api/v1/spool":
			if got := r.URL.Query().Get("tag"); got != "04:a2:b3:c4" {
				t.Errorf("tag query = %q, want %q", got, "04:a2:b3:c4")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id": 42, "remaining_weight": 712, "filament": {"name": "Galaxy Black", "material": "PLA"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewSpoolmanClient(server.URL, 5, "", "")
	spool, err := client.LookupSpoolByTagUID("04:a2:b3:c4")
	if err != nil {
		t.Fatalf("LookupSpoolByTagUID() error = %v", err)
	}
	if spool == nil || spool.ID != 42 || spool.Name != "Galaxy Black" {
		t.Fatalf("LookupSpoolByTagUID() = %+v, want normalized spool 42", spool)
	}
}

func TestLookupSpoolByTagUIDReturnsNilForUnassociatedTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/openapi.json":
			_, _ = w.Write([]byte(tagCapableOpenAPI))
		case "/api/v1/spool":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewSpoolmanClient(server.URL, 5, "", "")
	spool, err := client.LookupSpoolByTagUID("04A2B3C4")
	if err != nil {
		t.Fatalf("LookupSpoolByTagUID() error = %v", err)
	}
	if spool != nil {
		t.Fatalf("LookupSpoolByTagUID() = %+v, want nil", spool)
	}
}

func TestAssociateTagWithSpoolReportsUnsupportedOnLegacySpoolman(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/openapi.json" {
			t.Errorf("unexpected legacy Spoolman request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"paths":{"/spool":{"get":{"parameters":[]}}}}`))
	}))
	defer server.Close()

	client := NewSpoolmanClient(server.URL, 5, "", "")
	tag, err := client.AssociateTagWithSpool(42, "04A2B3C4", "openprinttag")
	if !errors.Is(err, ErrSpoolmanTagAPIUnsupported) {
		t.Fatalf("AssociateTagWithSpool() error = %v, want ErrSpoolmanTagAPIUnsupported", err)
	}
	if tag != nil {
		t.Fatalf("AssociateTagWithSpool() = %+v, want nil", tag)
	}
}
