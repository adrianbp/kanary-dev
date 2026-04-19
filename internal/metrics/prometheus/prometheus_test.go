package prometheus_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/metrics/prometheus"
)

func TestQuery_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "vector",
				"result": [{"metric": {}, "value": [1700000000, "0.42"]}]
			}
		}`))
	}))
	defer srv.Close()

	p := prometheus.New(srv.URL)
	res, err := p.Query(context.Background(), domain.MetricQuery{Name: "rps", Query: "sum(rate(requests[1m]))"})
	require.NoError(t, err)
	require.InDelta(t, 0.42, res.Value, 1e-9)
}

func TestQuery_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	p := prometheus.New(srv.URL)
	_, err := p.Query(context.Background(), domain.MetricQuery{Name: "x", Query: "missing"})
	require.ErrorIs(t, err, kerr.ErrNotFound)
}

func TestQuery_PrometheusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()

	p := prometheus.New(srv.URL)
	_, err := p.Query(context.Background(), domain.MetricQuery{Name: "x", Query: "bad"})
	require.ErrorContains(t, err, "bad query")
}

func TestQuery_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := prometheus.New(srv.URL)
	_, err := p.Query(context.Background(), domain.MetricQuery{Name: "x", Query: "up"})
	require.ErrorIs(t, err, kerr.ErrRetryable)
}

func TestHealthCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"1"]}]}}`))
	}))
	defer srv.Close()

	p := prometheus.New(srv.URL)
	require.NoError(t, p.HealthCheck(context.Background()))
}

func TestQuery_Unreachable(t *testing.T) {
	p := prometheus.New("http://127.0.0.1:19999")
	_, err := p.Query(context.Background(), domain.MetricQuery{Name: "x", Query: "up"})
	require.ErrorIs(t, err, kerr.ErrRetryable)
}
