package dynatrace_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/adrianbp/kanary-dev/internal/domain"
	kerr "github.com/adrianbp/kanary-dev/internal/errors"
	"github.com/adrianbp/kanary-dev/internal/metrics/dynatrace"
)

const validResponse = `{
	"resolution": "Inf",
	"result": [{
		"metricId": "ext:my.metric",
		"data": [{
			"timestamps": [1700000000000],
			"values": [0.42]
		}]
	}]
}`

func newProvider(srv *httptest.Server) *dynatrace.Provider {
	return dynatrace.New(srv.URL, "test-token")
}

func TestQuery_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validResponse))
	}))
	defer srv.Close()

	res, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{
		Name:   "rps",
		Query:  "ext:my.metric",
		Window: 5 * time.Minute,
	})
	require.NoError(t, err)
	require.InDelta(t, 0.42, res.Value, 1e-9)
	require.Equal(t, time.UnixMilli(1700000000000), res.Timestamp)
}

func TestQuery_AuthorizationHeaderSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validResponse))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.NoError(t, err)
	require.Equal(t, "Api-Token test-token", gotAuth)
}

func TestQuery_DefaultWindowWhenZero(t *testing.T) {
	var gotFrom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFrom = r.URL.Query().Get("from")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validResponse))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.NoError(t, err)
	require.Equal(t, "now-300s", gotFrom) // 5 min default
}

func TestQuery_LastNonNullValue(t *testing.T) {
	val := 7.0
	body := `{"resolution":"Inf","result":[{"metricId":"ext:x","data":[{
		"timestamps":[1000,2000,3000],
		"values":[null,` + "7.0" + `,null]
	}]}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.NoError(t, err)
	require.InDelta(t, val, res.Value, 1e-9)
}

func TestQuery_AllNullValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resolution":"Inf","result":[{"metricId":"ext:x","data":[{
			"timestamps":[1000],"values":[null]}]}]}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.ErrorIs(t, err, kerr.ErrNotFound)
}

func TestQuery_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resolution":"Inf","result":[]}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.ErrorIs(t, err, kerr.ErrNotFound)
}

func TestQuery_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.ErrorIs(t, err, kerr.ErrRetryable)
}

func TestQuery_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.Error(t, err)
	require.NotErrorIs(t, err, kerr.ErrRetryable) // auth errors are permanent
}

func TestQuery_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.Error(t, err)
	require.NotErrorIs(t, err, kerr.ErrRetryable)
}

func TestQuery_Unreachable(t *testing.T) {
	p := dynatrace.New("http://127.0.0.1:19999", "tok")
	_, err := p.Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.ErrorIs(t, err, kerr.ErrRetryable)
}

func TestQuery_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Query(context.Background(), domain.MetricQuery{Name: "x", Query: "ext:x"})
	require.ErrorContains(t, err, "unmarshal")
}

func TestHealthCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metrics":[]}`))
	}))
	defer srv.Close()

	require.NoError(t, newProvider(srv).HealthCheck(context.Background()))
}

func TestHealthCheck_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Forbidden"}}`))
	}))
	defer srv.Close()

	err := newProvider(srv).HealthCheck(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, kerr.ErrRetryable)
}

func TestHealthCheck_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := newProvider(srv).HealthCheck(context.Background())
	require.ErrorIs(t, err, kerr.ErrRetryable)
}
