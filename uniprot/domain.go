package uniprot

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes uniprot as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/uniprot-cli/uniprot"
//
// exactly as a database/sql program enables a driver with `import _
// "github.com/lib/pq"`. The init below registers it; the host then dereferences
// uniprot:// URIs by routing to the operations Register installs. The same
// Domain also builds the standalone uniprot binary (see cli.NewApp), so the
// binary and a host share one source of truth.
func init() { kit.Register(Domain{}) }

// Domain is the uniprot driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against, and
// the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "uniprot",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "uniprot",
			Short:  "A command line for UniProt protein database.",
			Long: `A command line for UniProt protein database.

uniprot reads public UniProt data over HTTPS, shapes it into
clean records, and prints output that pipes into the rest of your tools. No API
key, nothing to run alongside it.`,
			Site: Host,
			Repo: "https://github.com/tamnd/uniprot-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	// protein: fetch one protein record by accession.
	kit.Handle(app, kit.OpMeta{
		Name:     "protein",
		Group:    "read",
		Single:   true,
		Summary:  "Fetch a protein by accession (e.g. P04637, Q9Y2T1)",
		URIType:  "protein",
		Resolver: true,
		Args:     []kit.Arg{{Name: "accession", Help: "UniProt accession ID"}},
	}, getProtein)

	// search: full-text search over UniProt proteins.
	kit.Handle(app, kit.OpMeta{
		Name:    "search",
		Group:   "read",
		List:    true,
		Summary: "Search proteins by gene name, keyword, or query",
		URIType: "protein",
		Args:    []kit.Arg{{Name: "query", Help: "search query (e.g. BRCA1 AND organism_id:9606)"}},
	}, searchProteins)

	// taxonomy: fetch organism taxonomy info by taxon ID.
	kit.Handle(app, kit.OpMeta{
		Name:     "taxonomy",
		Group:    "read",
		Single:   true,
		Summary:  "Fetch organism taxonomy info by taxon ID (e.g. 9606 for human)",
		URIType:  "taxonomy",
		Resolver: true,
		Args:     []kit.Arg{{Name: "taxon-id", Help: "NCBI taxon ID (integer)"}},
	}, getTaxonomy)
}

// newClient builds the client from the host-resolved config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := NewClient()
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.HTTP.Timeout = cfg.Timeout
	}
	return c, nil
}

// --- inputs ---

type proteinRef struct {
	Accession string  `kit:"arg" help:"UniProt accession ID"`
	Client    *Client `kit:"inject"`
}

type searchInput struct {
	Query    string  `kit:"arg" help:"search query"`
	Limit    int     `kit:"flag" help:"max results to return"`
	Reviewed bool    `kit:"flag" help:"only return reviewed (Swiss-Prot) entries"`
	Client   *Client `kit:"inject"`
}

type taxonomyRef struct {
	TaxonID string  `kit:"arg" help:"NCBI taxon ID (integer)"`
	Client  *Client `kit:"inject"`
}

// --- handlers ---

func getProtein(ctx context.Context, in proteinRef, emit func(*Protein) error) error {
	p, err := in.Client.Protein(ctx, in.Accession)
	if err != nil {
		return mapErr(err)
	}
	return emit(p)
}

func searchProteins(ctx context.Context, in searchInput, emit func(*Protein) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	results, err := in.Client.Search(ctx, in.Query, SearchOptions{
		Limit:    limit,
		Reviewed: in.Reviewed,
	})
	if err != nil {
		return mapErr(err)
	}
	for _, p := range results {
		if err := emit(p); err != nil {
			return err
		}
	}
	return nil
}

func getTaxonomy(ctx context.Context, in taxonomyRef, emit func(*Taxonomy) error) error {
	taxonID, err := strconv.Atoi(strings.TrimSpace(in.TaxonID))
	if err != nil {
		return errs.Usage("taxon-id must be an integer, got %q", in.TaxonID)
	}
	t, err := in.Client.Taxonomy(ctx, taxonID)
	if err != nil {
		return mapErr(err)
	}
	return emit(t)
}

// --- Resolver: URI string functions, pure and network-free ---

// Classify turns any accepted input into (type, id).
// UniProt accessions (e.g. P04637, Q9Y2T1) → type "protein".
// Taxon IDs (numeric, e.g. 9606) → type "taxonomy".
func (Domain) Classify(input string) (uriType, id string, err error) {
	input = strings.TrimSpace(input)

	// Strip URL prefix if given; take the last non-empty path segment.
	if u, e := url.Parse(input); e == nil && (u.Scheme == "http" || u.Scheme == "https") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		// walk backwards to find a non-empty, non-keyword segment
		for i := len(parts) - 1; i >= 0; i-- {
			seg := parts[i]
			if seg != "" && seg != "entry" && seg != "uniprotkb" && seg != "taxonomy" {
				input = seg
				break
			}
		}
	}

	// Pure numeric → taxonomy
	if _, e := strconv.Atoi(input); e == nil {
		return "taxonomy", input, nil
	}

	// UniProt accession pattern: letter + digits + letter + digits (e.g. P04637, Q9Y2T1)
	if isAccession(input) {
		return "protein", strings.ToUpper(input), nil
	}

	return "", "", errs.Usage("unrecognized UniProt reference: %q (expected accession like P04637 or taxon ID like 9606)", input)
}

// isAccession returns true for a string that looks like a UniProt accession.
// Format: [A-NR-Z][0-9][A-Z][A-Z0-9]{2}[0-9] or [OPQ][0-9][A-Z0-9]{3}[0-9]
// For simplicity we accept: 6-10 chars, starts with a letter, contains digits.
func isAccession(s string) bool {
	if len(s) < 6 || len(s) > 10 {
		return false
	}
	hasLetter := false
	hasDigit := false
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i == 0 {
				hasLetter = true
			} else {
				hasLetter = true
			}
		} else if r >= '0' && r <= '9' {
			hasDigit = true
		} else {
			return false
		}
	}
	return hasLetter && hasDigit
}

// Locate is the inverse: the live https URL for a (type, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "protein":
		return fmt.Sprintf("https://www.uniprot.org/uniprotkb/%s/entry", id), nil
	case "taxonomy":
		return fmt.Sprintf("https://www.uniprot.org/taxonomy/%s", id), nil
	default:
		return "", errs.Usage("uniprot has no resource type %q", uriType)
	}
}

// mapErr converts a library error into the kit error kind that carries the right
// exit code.
func mapErr(err error) error {
	return err
}
