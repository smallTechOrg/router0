package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchOpenRouterBalance(t *testing.T) {
	var keyBody, creditsBody string
	creditsStatus := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or-test" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"no"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/key":
			_, _ = w.Write([]byte(keyBody))
		case "/v1/credits":
			w.WriteHeader(creditsStatus)
			_, _ = w.Write([]byte(creditsBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("OPENROUTER_BASE_URL", srv.URL+"/v1")
	ctx := context.Background()

	keyBody = `{"data":{"limit":10,"usage":4,"limit_remaining":6}}`
	b, err := FetchOpenRouterBalance(ctx, "sk-or-test")
	if err != nil || b.RemainingUSD == nil || *b.RemainingUSD != 6 || *b.LimitUSD != 10 || b.UsageUSD != 4 {
		t.Fatalf("limited key: %+v %v", b, err)
	}

	keyBody = `{"data":{"limit":null,"usage":1.5,"limit_remaining":null}}`
	creditsBody = `{"data":{"total_credits":5,"total_usage":5}}`
	b, err = FetchOpenRouterBalance(ctx, "sk-or-test")
	if err != nil || b.RemainingUSD == nil || *b.RemainingUSD != 0 || b.LimitUSD != nil {
		t.Fatalf("unlimited key, account credits: %+v %v", b, err)
	}

	creditsStatus = 403
	creditsBody = `{"error":{"code":403,"message":"forbidden"}}`
	b, err = FetchOpenRouterBalance(ctx, "sk-or-test")
	if err != nil || b.RemainingUSD != nil {
		t.Fatalf("credits failure should leave remaining unknown: %+v %v", b, err)
	}

	if _, err := FetchOpenRouterBalance(ctx, "wrong"); err == nil {
		t.Fatal("bad key: want error")
	} else if pe, ok := err.(*ProviderError); !ok || pe.Class != ErrorClassAuth {
		t.Fatalf("bad key: want auth ProviderError, got %#v", err)
	}
}
