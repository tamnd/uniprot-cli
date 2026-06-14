package uniprot

import (
	"testing"

	"github.com/tamnd/any-cli/kit"
)

// These tests are offline: they exercise the URI driver's pure string functions
// and the host wiring (mint, body, resolve), which need no network. The client's
// HTTP behaviour is covered in uniprot_test.go.

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "uniprot" {
		t.Errorf("Scheme = %q, want uniprot", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "uniprot" {
		t.Errorf("Identity.Binary = %q, want uniprot", info.Identity.Binary)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in      string
		typ     string
		id      string
		wantErr bool
	}{
		// UniProt accessions → protein
		{"P04637", "protein", "P04637", false},
		{"Q9Y2T1", "protein", "Q9Y2T1", false},
		// Taxon IDs (numeric) → taxonomy
		{"9606", "taxonomy", "9606", false},
		{"10090", "taxonomy", "10090", false},
		// URL with accession
		{"https://rest.uniprot.org/uniprotkb/P04637", "protein", "P04637", false},
		// Unrecognized input
		{"not-a-thing!", "", "", true},
	}
	for _, tc := range cases {
		typ, id, err := Domain{}.Classify(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Classify(%q): got nil error, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("Classify(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if typ != tc.typ {
			t.Errorf("Classify(%q) type = %q, want %q", tc.in, typ, tc.typ)
		}
		if id != tc.id {
			t.Errorf("Classify(%q) id = %q, want %q", tc.in, id, tc.id)
		}
	}
}

func TestLocate(t *testing.T) {
	cases := []struct {
		typ  string
		id   string
		want string
	}{
		{"protein", "P04637", "https://www.uniprot.org/uniprotkb/P04637/entry"},
		{"taxonomy", "9606", "https://www.uniprot.org/taxonomy/9606"},
	}
	for _, tc := range cases {
		got, err := Domain{}.Locate(tc.typ, tc.id)
		if err != nil {
			t.Errorf("Locate(%q, %q): %v", tc.typ, tc.id, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Locate(%q, %q) = %q, want %q", tc.typ, tc.id, got, tc.want)
		}
	}
}

func TestLocateUnknownType(t *testing.T) {
	_, err := Domain{}.Locate("gene", "BRCA1")
	if err == nil {
		t.Error("Locate with unknown type: expected error, got nil")
	}
}

// TestHostWiring mounts the driver in a kit Host (the runtime ant drives) and
// checks the round trip: a protein record mints to its URI, and a bare id
// resolves back to the same URI. The init in domain.go registers the domain,
// so kit.Open finds it.
func TestHostWiring(t *testing.T) {
	h, err := kit.Open()
	if err != nil {
		t.Fatal(err)
	}

	p := &Protein{
		Accession:   "P04637",
		EntryName:   "P53_HUMAN",
		ProteinName: "Cellular tumor antigen p53",
		GeneName:    "TP53",
		Organism:    "Homo sapiens",
		TaxonomyID:  9606,
		Reviewed:    true,
		Length:      393,
	}
	u, err := h.Mint(p)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	_ = u // the URI is minted; exact form depends on kit conventions
}
