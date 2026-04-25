// Package dynatrace implements the metrics.Provider interface using the
// Dynatrace Metrics v2 API (/api/v2/metrics/query).
package dynatrace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
)

const (
	metricsQueryPath = "/api/v2/metrics/query"
	metricsListPath  = "/api/v2/metrics"
)

// Provider queries a Dynatrace environment via the Metrics v2 API.
type Provider struct {
	address  string
	apiToken string
	client   *http.Client
}

// New returns a Provider for the Dynatrace environment at address,
// authenticated with apiToken.
func New(address, apiToken string) *Provider {
	return &Provider{
		address:  address,
		apiToken: apiToken,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Query executes a Dynatrace metric selector over q.Window and returns the
// last non-null scalar value in the response series.
func (p *Provider) Query(ctx context.Context, q domain.MetricQuery) (domain.MetricResult, error) {
	window := q.Window
	if window == 0 {
		window = 5 * time.Minute
	}

	params := url.Values{}
	params.Set("metricSelector", q.Query)
	params.Set("from", fmt.Sprintf("now-%ds", int(window.Seconds())))
	params.Set("to", "now")
	params.Set("resolution", "Inf") // collapse to one data point

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.address+metricsQueryPath+"?"+params.Encode(), nil)
	if err != nil {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("build request: %w", err))
	}
	req.Header.Set("Authorization", "Api-Token "+p.apiToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("dynatrace query: %w", err))
	}
	defer resp.Body.Close() //#nosec G307 -- close after read, error ignored intentionally

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("read response: %w", err))
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return domain.MetricResult{}, fmt.Errorf("dynatrace auth error: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return domain.MetricResult{}, kerr.Retryable(fmt.Errorf("dynatrace returned %d: %s", resp.StatusCode, body))
	}

	return parseQueryResponse(body)
}

// HealthCheck verifies connectivity and token validity by listing one metric.
func (p *Provider) HealthCheck(ctx context.Context) error {
	params := url.Values{}
	params.Set("pageSize", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.address+metricsListPath+"?"+params.Encode(), nil)
	if err != nil {
		return kerr.Retryable(fmt.Errorf("build health request: %w", err))
	}
	req.Header.Set("Authorization", "Api-Token "+p.apiToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return kerr.Retryable(fmt.Errorf("dynatrace health check: %w", err))
	}
	defer resp.Body.Close() //#nosec G307 -- close after read, error ignored intentionally

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("dynatrace auth error %d: %s", resp.StatusCode, body)
	}
	if resp.StatusCode != http.StatusOK {
		return kerr.Retryable(fmt.Errorf("dynatrace health check returned %d: %s", resp.StatusCode, body))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dynatrace Metrics v2 API response parsing
// ---------------------------------------------------------------------------

type metricsQueryResponse struct {
	Resolution string         `json:"resolution"`
	Result     []metricResult `json:"result"`
}

type metricResult struct {
	MetricID string       `json:"metricId"`
	Data     []metricData `json:"data"`
}

type metricData struct {
	Timestamps []int64    `json:"timestamps"`
	Values     []*float64 `json:"values"` // nullable per Dynatrace spec
}

func parseQueryResponse(body []byte) (domain.MetricResult, error) {
	var r metricsQueryResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return domain.MetricResult{}, fmt.Errorf("unmarshal dynatrace response: %w", err)
	}
	if len(r.Result) == 0 || len(r.Result[0].Data) == 0 {
		return domain.MetricResult{}, fmt.Errorf("%w: dynatrace returned no metric data", kerr.ErrNotFound)
	}

	data := r.Result[0].Data[0]
	// Scan backwards to find the last non-null value; Dynatrace can emit
	// trailing nulls when the resolution window extends past available data.
	for i := len(data.Values) - 1; i >= 0; i-- {
		if data.Values[i] != nil {
			ts := time.Now()
			if i < len(data.Timestamps) {
				ts = time.UnixMilli(data.Timestamps[i])
			}
			return domain.MetricResult{
				Value:     *data.Values[i],
				Timestamp: ts,
			}, nil
		}
	}

	return domain.MetricResult{}, fmt.Errorf("%w: all dynatrace data points are null", kerr.ErrNotFound)
}
