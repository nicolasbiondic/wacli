package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
)

// LENZ Ops calls these through vendas-chat-gateway. Empty disposable stores
// must reach the command's own safety checks, not reject the CLI contract.
func TestLENZBusinessCLICompatibility(t *testing.T) {
	for _, args := range [][]string{
		{"profile", "business", "get"},
		{"profile", "business", "set", "--description", "LENZ", "--address", "Lima", "--email", "ventas@lenz.pe", "--website", "https://lenz.pe", "--hours-timezone", "America/Lima", "--hours", "mon=09:00-18:00"},
		{"profile", "business", "set", "--website", "", "--hours-timezone", "America/Lima", "--hours-closed"},
		{"catalog", "list", "--limit", "50", "--after", "next-page"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			argv := append([]string{"--store", t.TempDir(), "--read-only", "--json", "--timeout", "1000ms"}, args...)
			err := execute(argv)
			if err == nil || (!strings.Contains(err.Error(), "read-only") && !strings.Contains(err.Error(), "not linked")) {
				t.Fatalf("CLI contract rejected: %v", err)
			}
		})
	}
}

func TestLENZBusinessCLIDelegatedJSON(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, set := range []bool{false, true} {
		t.Run(map[bool]string{false: "get", true: "set"}[set], func(t *testing.T) {
			dir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			requests := make(chan sendDelegateRequest, 1)
			client := &businessFake{linked: "51905448483@s.whatsapp.net"}
			stop, err := startSendDelegateServerForStore(t.Context(), dir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				requests <- req
				if req.Kind == businessProfileGetKind {
					return executeBusinessProfileGet(ctx, client, req)
				}
				return executeBusinessProfileSet(ctx, client, req)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := []string{"--store", dir, "--json", "--timeout", "1000ms", "profile", "business", "get"}
			if set {
				args[7] = "set"
				args = append(args, "--description", "LENZ", "--address", "Lima", "--email", "ventas@lenz.pe", "--website", "https://lenz.pe", "--website", "https://example.com", "--hours-timezone", "America/Lima", "--hours", "mon=09:00-18:00", "--hours", "sat=open_24h", "--hours", "sun=appointment_only")
			}
			stdout := captureRootStdout(t, func() {
				if err := execute(args); err != nil {
					t.Fatal(err)
				}
			})
			got := <-requests
			wantKind := businessProfileGetKind
			if set {
				wantKind = businessProfileSetKind
			}
			if got.Kind != wantKind || got.Version != 1 || got.TimeoutMS != 1000 || got.DeadlineUnixMS == 0 {
				t.Fatalf("IPC contract changed: %+v", got)
			}
			if set && (got.BusinessProfile == nil || *got.BusinessProfile.Description != "LENZ" || *got.BusinessProfile.Address != "Lima" || *got.BusinessProfile.Email != "ventas@lenz.pe" || len(got.BusinessProfile.Websites) != 2 || got.BusinessProfile.Hours.Days[0].OpenTime != "540") {
				t.Fatalf("delta lost: %+v", got.BusinessProfile)
			}
			var envelope struct {
				Success bool           `json:"success"`
				Data    map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || !envelope.Success {
				t.Fatalf("invalid envelope: %s (%v)", stdout, err)
			}
			profile := envelope.Data
			if set {
				if profile["updated"] != true {
					t.Fatalf("set contract changed: %s", stdout)
				}
				profile = profile["profile"].(map[string]any)
			}
			for _, key := range []string{"jid", "description", "address", "email", "websites", "categories", "business_hours_timezone", "business_hours", "profile_options"} {
				if _, ok := profile[key]; !ok {
					t.Errorf("missing LENZ field %s: %s", key, stdout)
				}
			}
		})
	}
}

type catalogRoundTripper func(*http.Request) (*http.Response, error)

func (f catalogRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestLENZCatalogCLIJSON(t *testing.T) {
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://graph.whatsapp.com/graphql/catalog" {
			t.Fatalf("unexpected network destination: %s", req.URL)
		}
		var body struct {
			Variables struct {
				Request struct {
					Catalog map[string]any `json:"product_catalog"`
				} `json:"request"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Variables.Request.Catalog["limit"] != "50" || body.Variables.Request.Catalog["after"] != "next-page" {
			t.Fatalf("pagination changed: %+v", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"xwa_product_catalog_get_product_catalog":{"product_catalog":{"products":[{"id":"p1","name":"Camera","price":"1500500","currency":"PEN"}],"paging":{"after":"page-3"}}}}}`)), Header: http.Header{}}, nil
	})
	stdout := captureRootStdout(t, func() {
		// --jid avoids opening any session store; all other arguments are the
		// gateway's exact read-only, deadline and pagination invocation.
		if err := execute([]string{"--store", t.TempDir(), "--read-only", "--json", "--timeout", "1000ms", "catalog", "list", "--limit", "50", "--after", "next-page", "--jid", "51905448483@s.whatsapp.net"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, field := range []string{`"success":true`, `"has_catalog":true`, `"next_cursor":"page-3"`, `"price_amount_1000":1500500`, `"price":"1500.50"`, `"retailer_id":`, `"image_url":`, `"original_image_url":`, `"review_status":`, `"hidden":`, `"availability":`} {
		if !strings.Contains(stdout, field) {
			t.Errorf("missing LENZ catalog field %s: %s", field, stdout)
		}
	}
}
