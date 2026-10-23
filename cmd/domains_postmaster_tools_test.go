package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postmasterToolsServer answers OAuth, the domain list, dns_setup_link (recording
// the flow it was asked for) and domain verify with the given postmaster state.
func postmasterToolsServer(t *testing.T, postmaster string, flows *[]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/service_accounts/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"jwt-test-session","token_type":"Bearer","expires_in":3600}`))
		case r.Header.Get("Authorization") != "Bearer jwt-test-session":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == "GET" && r.URL.Path == "/v1/environments/456/domains":
			_, _ = w.Write([]byte(`{"domains":[{"id":"789","domain":"mail.example.com"}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dns_setup_link"):
			*flows = append(*flows, r.URL.Query().Get("flow"))
			_, _ = w.Write([]byte(`{"handoff_token":"test.handoff.token","expires_in":300}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/verify"):
			_, _ = w.Write([]byte(`{"domain":{"id":"789","domain":"mail.example.com","verified":true}` + postmaster + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func setupPostmasterToolsTest(t *testing.T, postmaster string) (*httptest.Server, *[]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CIO_TOKEN", "sa_live_test123")
	t.Setenv("CIO_ACCESS_TOKEN", "")
	t.Setenv("CIO_UI_URL", "")
	resetDomainFlags()
	flows := []string{}
	return postmasterToolsServer(t, postmaster, &flows), &flows
}

func TestDomains_PostmasterTools_Configure(t *testing.T) {
	server, flows := setupPostmasterToolsTest(t, "")

	stdout, stderr, err := executeCommand("domains", "postmaster_tools", "configure",
		"--env-id", "456", "mail.example.com", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*flows) != 1 || (*flows)[0] != "postmaster_tools" {
		t.Errorf("expected one dns_setup_link call with flow=postmaster_tools, got %v", *flows)
	}
	if !strings.Contains(stderr, "/cli/dns-setup#test.handoff.token") {
		t.Errorf("expected the setup URL in stderr, got: %s", stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON on stdout: %v\nstdout: %s", err, stdout)
	}
	if result["domain"] != "mail.example.com" || result["domain_id"] != "789" {
		t.Errorf("unexpected stdout: %v", result)
	}
}

func TestDomains_Configure_StillMintsDomainAuth(t *testing.T) {
	server, flows := setupPostmasterToolsTest(t, "")

	if _, _, err := executeCommand("domains", "configure",
		"--env-id", "456", "mail.example.com", "--api-url", server.URL); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*flows) != 1 || (*flows)[0] != "domain_auth" {
		t.Errorf("expected flow=domain_auth, got %v", *flows)
	}
}

func TestDomains_PostmasterTools_Configure_DryRun(t *testing.T) {
	server, flows := setupPostmasterToolsTest(t, "")

	stdout, _, err := executeCommand("domains", "postmaster_tools", "configure",
		"--env-id", "456", "mail.example.com", "--dry-run", "--api-url", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*flows) != 0 {
		t.Errorf("dry run must not mint a link, got %v", *flows)
	}
	if !strings.Contains(stdout, `"flow": "postmaster_tools"`) && !strings.Contains(stdout, `"flow":"postmaster_tools"`) {
		t.Errorf("expected the flow in the dry-run output, got: %s", stdout)
	}
}

func TestDomains_PostmasterTools_Verify(t *testing.T) {
	cases := []struct {
		name         string
		postmaster   string
		wantStatus   string
		wantVerified bool
		wantStderr   string
	}{
		{
			name:         "verified",
			postmaster:   `,"postmaster":{"root":"example.com","status":"verified"}`,
			wantStatus:   "verified",
			wantVerified: true,
			wantStderr:   "Postmaster Tools record for example.com: verified.",
		},
		{
			name:       "pending shows the record",
			postmaster: `,"postmaster":{"root":"example.com","status":"pending","record":{"domain":"example.com","type":"TXT","value":"google-site-verification=abc"}}`,
			wantStatus: "pending",
			wantStderr: "google-site-verification=abc",
		},
		{
			name:       "not added",
			postmaster: `,"postmaster":{"root":"example.com","status":"not_added"}`,
			wantStatus: "not_added",
			wantStderr: "isn't in Postmaster Tools yet",
		},
		{
			name:       "not set up for the workspace",
			postmaster: "",
			wantStatus: "unavailable",
			wantStderr: "Postmaster Tools isn't set up",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := setupPostmasterToolsTest(t, tc.postmaster)

			stdout, stderr, err := executeCommand("domains", "postmaster_tools", "verify",
				"--env-id", "456", "mail.example.com", "--api-url", server.URL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("invalid JSON on stdout: %v\nstdout: %s", err, stdout)
			}
			if result["status"] != tc.wantStatus || result["verified"] != tc.wantVerified {
				t.Errorf("expected status=%s verified=%v, got %v", tc.wantStatus, tc.wantVerified, result)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("expected %q in stderr, got: %s", tc.wantStderr, stderr)
			}
		})
	}
}
