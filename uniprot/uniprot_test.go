package uniprot_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tamnd/uniprot-cli/uniprot"
)

// mockProteinJSON is a minimal UniProt protein wire payload.
const mockProteinJSON = `{
  "primaryAccession": "P04637",
  "uniProtkbId": "P53_HUMAN",
  "proteinDescription": {
    "recommendedName": {
      "fullName": {"value": "Cellular tumor antigen p53"}
    }
  },
  "genes": [{"geneName": {"value": "TP53"}}],
  "organism": {"scientificName": "Homo sapiens", "taxonId": 9606},
  "entryType": "UniProtKB reviewed (Swiss-Prot)",
  "sequence": {"length": 393}
}`

// mockSearchJSON wraps one protein as a search result.
const mockSearchJSON = `{
  "results": [` + mockProteinJSON + `]
}`

// mockTaxonomyJSON is a minimal taxonomy wire payload.
const mockTaxonomyJSON = `{
  "taxonId": 9606,
  "scientificName": "Homo sapiens",
  "commonName": "Human",
  "lineages": ["Eukaryota", "Metazoa", "Homo"]
}`

func newTestClient(ts *httptest.Server) *uniprot.Client {
	c := uniprot.NewClient()
	c.BaseURL = ts.URL
	c.Rate = 0
	return c
}

// TestProteinGet verifies that Protein() fetches and flattens a protein record.
func TestProteinGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/uniprotkb/P04637.json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockProteinJSON))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	p, err := c.Protein(context.Background(), "P04637")
	if err != nil {
		t.Fatalf("Protein: %v", err)
	}
	if p.Accession != "P04637" {
		t.Errorf("Accession = %q, want P04637", p.Accession)
	}
	if p.EntryName != "P53_HUMAN" {
		t.Errorf("EntryName = %q, want P53_HUMAN", p.EntryName)
	}
	if p.ProteinName != "Cellular tumor antigen p53" {
		t.Errorf("ProteinName = %q", p.ProteinName)
	}
	if p.GeneName != "TP53" {
		t.Errorf("GeneName = %q, want TP53", p.GeneName)
	}
	if p.Organism != "Homo sapiens" {
		t.Errorf("Organism = %q, want Homo sapiens", p.Organism)
	}
	if p.TaxonomyID != 9606 {
		t.Errorf("TaxonomyID = %d, want 9606", p.TaxonomyID)
	}
	if !p.Reviewed {
		t.Error("Reviewed = false, want true (Swiss-Prot entry)")
	}
	if p.Length != 393 {
		t.Errorf("Length = %d, want 393", p.Length)
	}
}

// TestSearch verifies that Search() sends the right query param and returns results.
func TestSearch(t *testing.T) {
	var capturedQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockSearchJSON))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	results, err := c.Search(context.Background(), "BRCA1 AND organism_id:9606", uniprot.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Accession != "P04637" {
		t.Errorf("result[0].Accession = %q, want P04637", results[0].Accession)
	}
	if !strings.Contains(capturedQuery, "BRCA1") {
		t.Errorf("query %q does not contain BRCA1", capturedQuery)
	}
}

// TestSearchReviewed verifies that the --reviewed flag appends "AND reviewed:true".
func TestSearchReviewed(t *testing.T) {
	var capturedQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockSearchJSON))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.Search(context.Background(), "TP53", uniprot.SearchOptions{Reviewed: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(capturedQuery, "reviewed:true") {
		t.Errorf("query %q does not contain reviewed:true", capturedQuery)
	}
}

// TestTaxonomy verifies that Taxonomy() fetches and maps organism info.
func TestTaxonomy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/taxonomy/9606" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockTaxonomyJSON))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	tax, err := c.Taxonomy(context.Background(), 9606)
	if err != nil {
		t.Fatalf("Taxonomy: %v", err)
	}
	if tax.TaxonID != 9606 {
		t.Errorf("TaxonID = %d, want 9606", tax.TaxonID)
	}
	if tax.Scientific != "Homo sapiens" {
		t.Errorf("Scientific = %q, want Homo sapiens", tax.Scientific)
	}
	if tax.Common != "Human" {
		t.Errorf("Common = %q, want Human", tax.Common)
	}
	if !strings.Contains(tax.Lineage, "Eukaryota") {
		t.Errorf("Lineage %q does not contain Eukaryota", tax.Lineage)
	}
}

// TestRetryOn503 verifies that the client retries on 5xx responses.
func TestRetryOn503(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockProteinJSON))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	c.Retries = 5
	p, err := c.Protein(context.Background(), "P04637")
	if err != nil {
		t.Fatalf("Protein after retry: %v", err)
	}
	if p.Accession != "P04637" {
		t.Errorf("Accession = %q, want P04637", p.Accession)
	}
	if hits != 3 {
		t.Errorf("server hit %d times, want 3", hits)
	}
}

// TestUserAgentAndAcceptHeader verifies the client sends proper headers.
func TestUserAgentAndAcceptHeader(t *testing.T) {
	var gotUA, gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mockProteinJSON))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.Protein(context.Background(), "P04637")
	if err != nil {
		t.Fatalf("Protein: %v", err)
	}
	if gotUA == "" {
		t.Error("User-Agent header was empty")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
}

// TestSearchQueryParam verifies query and size are sent as URL params.
func TestSearchQueryParam(t *testing.T) {
	var capturedSize string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSize = r.URL.Query().Get("size")
		// Return valid search JSON
		resp := map[string]interface{}{
			"results": []interface{}{
				json.RawMessage(mockProteinJSON),
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	c := newTestClient(ts)
	_, err := c.Search(context.Background(), "TP53", uniprot.SearchOptions{Limit: 7})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if capturedSize != "7" {
		t.Errorf("size param = %q, want 7", capturedSize)
	}
}
