// Package uniprot is the library behind the uniprot command line:
// the HTTP client, request shaping, and the typed data models for the
// UniProt REST API.
//
// The Client here is the spine every command shares. It sets a real
// User-Agent, paces requests so a busy session stays polite, and retries the
// transient failures (429 and 5xx) that any public API throws under load.
package uniprot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultUserAgent identifies the client to UniProt servers.
const DefaultUserAgent = "uniprot-cli/0.1.0 (github.com/tamnd/uniprot-cli)"

// Host is the site this client talks to.
const Host = "rest.uniprot.org"

// BaseURL is the root every request is built from.
const BaseURL = "https://" + Host

// Config holds all tunable client parameters.
type Config struct {
	BaseURL   string
	UserAgent string
	Rate      time.Duration
	Timeout   time.Duration
	Retries   int
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		BaseURL:   BaseURL,
		UserAgent: DefaultUserAgent,
		Rate:      200 * time.Millisecond,
		Timeout:   30 * time.Second,
		Retries:   3,
	}
}

// Client talks to the UniProt REST API over HTTP.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	BaseURL   string
	// Rate is the minimum gap between requests. Zero means no pacing.
	Rate    time.Duration
	Retries int

	mu   sync.Mutex
	last time.Time
}

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	cfg := DefaultConfig()
	return &Client{
		HTTP:      &http.Client{Timeout: cfg.Timeout},
		UserAgent: cfg.UserAgent,
		BaseURL:   cfg.BaseURL,
		Rate:      cfg.Rate,
		Retries:   cfg.Retries,
	}
}

// NewClientFromConfig returns a Client configured from cfg.
func NewClientFromConfig(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = BaseURL
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	retries := cfg.Retries
	if retries == 0 {
		retries = 3
	}
	return &Client{
		HTTP:      &http.Client{Timeout: timeout},
		UserAgent: cfg.UserAgent,
		BaseURL:   cfg.BaseURL,
		Rate:      cfg.Rate,
		Retries:   retries,
	}
}

// --- output models ---

// Protein is a flattened, output-friendly UniProt protein record.
type Protein struct {
	Accession   string `json:"accession" kit:"id"`
	EntryName   string `json:"entry_name"`
	ProteinName string `json:"protein_name"`
	GeneName    string `json:"gene_name,omitempty"`
	Organism    string `json:"organism"`
	TaxonomyID  int    `json:"taxonomy_id,omitempty"`
	Reviewed    bool   `json:"reviewed"` // Swiss-Prot vs TrEMBL
	Length      int    `json:"length,omitempty"`
	Function    string `json:"function,omitempty"`
}

// Taxonomy holds organism taxonomy info from UniProt.
type Taxonomy struct {
	TaxonID    int    `json:"taxon_id"`
	Scientific string `json:"scientific_name"`
	Common     string `json:"common_name,omitempty"`
	Lineage    string `json:"lineage,omitempty"`
}

// --- wire types (UniProt REST JSON structure) ---

type wireFullName struct {
	Value string `json:"value"`
}

type wireProteinDescription struct {
	RecommendedName struct {
		FullName wireFullName `json:"fullName"`
	} `json:"recommendedName"`
}

type wireGene struct {
	GeneName struct {
		Value string `json:"value"`
	} `json:"geneName"`
}

type wireOrganism struct {
	ScientificName string `json:"scientificName"`
	TaxonId        int    `json:"taxonId"`
}

type wireSequence struct {
	Length int `json:"length"`
}

type wireComment struct {
	CommentType string `json:"commentType"`
	Texts       []struct {
		Value string `json:"value"`
	} `json:"texts"`
}

// wireProtein is the raw JSON structure returned by UniProt.
type wireProtein struct {
	PrimaryAccession   string                 `json:"primaryAccession"`
	UniProtkbId        string                 `json:"uniProtkbId"`
	ProteinDescription wireProteinDescription `json:"proteinDescription"`
	Genes              []wireGene             `json:"genes"`
	Organism           wireOrganism           `json:"organism"`
	EntryType          string                 `json:"entryType"`
	Sequence           wireSequence           `json:"sequence"`
	Comments           []wireComment          `json:"comments"`
}

// wireSearchResult is the wrapper around search results.
type wireSearchResult struct {
	Results []wireProtein `json:"results"`
}

// wireTaxonomy is the raw JSON structure returned by UniProt taxonomy endpoint.
type wireTaxonomy struct {
	TaxonId        int      `json:"taxonId"`
	ScientificName string   `json:"scientificName"`
	CommonName     string   `json:"commonName"`
	Lineages       []string `json:"lineages"`
}

// --- conversion helpers ---

func flattenProtein(w wireProtein) *Protein {
	p := &Protein{
		Accession:   w.PrimaryAccession,
		EntryName:   w.UniProtkbId,
		ProteinName: w.ProteinDescription.RecommendedName.FullName.Value,
		Organism:    w.Organism.ScientificName,
		TaxonomyID:  w.Organism.TaxonId,
		Reviewed:    strings.Contains(w.EntryType, "reviewed"),
		Length:      w.Sequence.Length,
	}
	if len(w.Genes) > 0 {
		p.GeneName = w.Genes[0].GeneName.Value
	}
	for _, c := range w.Comments {
		if c.CommentType == "FUNCTION" && len(c.Texts) > 0 {
			p.Function = c.Texts[0].Value
			break
		}
	}
	return p
}

// --- client methods ---

// Protein fetches a single protein by accession (e.g. "P04637").
func (c *Client) Protein(ctx context.Context, accession string) (*Protein, error) {
	u := c.BaseURL + "/uniprotkb/" + url.PathEscape(accession) + ".json"
	body, err := c.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	var w wireProtein
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("parse protein %s: %w", accession, err)
	}
	return flattenProtein(w), nil
}

// SearchOptions controls the search query.
type SearchOptions struct {
	Limit    int
	Reviewed bool
}

// Search searches UniProt proteins by query string.
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) ([]*Protein, error) {
	q := query
	if opts.Reviewed {
		q = q + " AND reviewed:true"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	u := c.BaseURL + "/uniprotkb/search?query=" + url.QueryEscape(q) +
		"&format=json&size=" + fmt.Sprintf("%d", limit)

	body, err := c.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	var result wireSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	out := make([]*Protein, 0, len(result.Results))
	for _, w := range result.Results {
		out = append(out, flattenProtein(w))
	}
	return out, nil
}

// Taxonomy fetches organism taxonomy info by taxon ID (e.g. 9606 for human).
func (c *Client) Taxonomy(ctx context.Context, taxonID int) (*Taxonomy, error) {
	u := fmt.Sprintf("%s/taxonomy/%d", c.BaseURL, taxonID)
	body, err := c.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	var w wireTaxonomy
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("parse taxonomy %d: %w", taxonID, err)
	}
	t := &Taxonomy{
		TaxonID:    w.TaxonId,
		Scientific: w.ScientificName,
		Common:     w.CommonName,
	}
	if len(w.Lineages) > 0 {
		t.Lineage = strings.Join(w.Lineages, " > ")
	}
	return t, nil
}

// Get fetches the URL and returns the response body. It paces and retries
// according to the client's settings.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", rawURL, lastErr)
}

func (c *Client) do(ctx context.Context, rawURL string) (body []byte, retry bool, err error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// pace blocks until at least Rate has passed since the previous request.
func (c *Client) pace() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Rate <= 0 {
		return
	}
	if wait := c.Rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
