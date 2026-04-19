// Package prometheus implements the metrics.Provider interface using the
// Prometheus HTTP API v1 (/api/v1/query).
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

// Provider queries a Prometheus-compatible endpoint.
type Provider struct {
	address string
	client  *http.Client
}

// New returns a Provider that talks to the Prometheus instance at address.
func New(address string) *Provider {
	return &Provider{
		address: address,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Query executes an instant PromQL query at the end of q.Window.
func (p *Provider) Query(ctx context.Context, q domain.MetricQuery) (domain.MetricResult, error) {
	endpoint := p.address + "/api/v1/query"

	params := url.Values{}
	params.Set("query", q.Query)
	params.Set("time", strconv.FormatInt(time.Now().Unix(), 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("build request: %w", err))
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("prometheus query: %w", err))
	}
	defer resp.Body.Close() //#nosec G307 -- close after read, error ignored intentionally

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("read response: %w", err))
	}

	if resp.StatusCode != http.StatusOK {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("prometheus returned %d: %s", resp.StatusCode, body))
	}

	return parseQueryResponse(body)
}

// HealthCheck calls /api/v1/query with a trivial expression to verify connectivity.
func (p *Provider) HealthCheck(ctx context.Context) error {
	_, err := p.Query(ctx, domain.MetricQuery{Name: "health", Query: "1"})
	return err
}

// ---------------------------------------------------------------------------
// Prometheus API response parsing
// ---------------------------------------------------------------------------

type apiResponse struct {
	Status string    `json:"status"`
	Data   queryData `json:"data"`
	Error  string    `json:"error,omitempty"`
}

type queryData struct {
	ResultType string        `json:"resultType"`
	Result     []vectorEntry `json:"result"`
}

type vectorEntry struct {
	// Value is [timestamp, "stringValue"].
	Value []json.RawMessage `json:"value"`
}

func parseQueryResponse(body []byte) (domain.MetricResult, error) {
	var r apiResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return domain.MetricResult{}, fmt.Errorf("unmarshal prometheus response: %w", err)
	}
	if r.Status != "success" {
		return domain.MetricResult{}, fmt.Errorf("prometheus error: %s", r.Error)
	}
	if len(r.Data.Result) == 0 {
		return domain.MetricResult{}, fmt.Errorf("%w: query returned no results", kerr.ErrNotFound)
	}

	entry := r.Data.Result[0]
	if len(entry.Value) != 2 {
		return domain.MetricResult{}, fmt.Errorf("unexpected value tuple length %d", len(entry.Value))
	}

	// entry.Value[0] is the unix timestamp (float), entry.Value[1] is the string value.
	var tsFloat float64
	if err := json.Unmarshal(entry.Value[0], &tsFloat); err != nil {
		return domain.MetricResult{}, fmt.Errorf("parse timestamp: %w", err)
	}

	var valStr string
	if err := json.Unmarshal(entry.Value[1], &valStr); err != nil {
		return domain.MetricResult{}, fmt.Errorf("parse value: %w", err)
	}

	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return domain.MetricResult{}, fmt.Errorf("parse metric value %q: %w", valStr, err)
	}

	return domain.MetricResult{
		Value:     val,
		Timestamp: time.Unix(int64(tsFloat), 0), //#nosec G115 -- unix timestamp fits int64
	}, nil
}
