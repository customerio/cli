package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/skills"
)

// planServer mimics the skills endpoint: the body is scoped to the caller's
// plan, the ETag is sha256 of that body, and a matching If-None-Match gets a
// 304. An unauthenticated caller never sees plan-gated skills.
// The token exchange answers with exchangeStatus.
type planServer struct {
	*httptest.Server
	upgraded       atomic.Bool
	exchangeStatus atomic.Int32
	skillFetches   atomic.Int32
}

func newPlanServer(t *testing.T) *planServer {
	t.Helper()
	s := &planServer{}
	s.exchangeStatus.Store(http.StatusOK)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service_accounts/oauth/token":
			if st := int(s.exchangeStatus.Load()); st != http.StatusOK {
				w.WriteHeader(st)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"jwt-plan","token_type":"Bearer","expires_in":3600}`))
		case "/v1/agent/skills":
			s.skillFetches.Add(1)
			fly := skills.Skill{Path: "fly-api", Name: "Fly", Content: "fly", Files: map[string]string{"basics.md": "b"}}
			resp := skills.SkillsResponse{Prompt: "p", Skills: []skills.Skill{fly}}
			if r.Header.Get("Authorization") == "Bearer jwt-plan" && s.upgraded.Load() {
				resp.Skills[0].Files = map[string]string{"basics.md": "b", "gated.md": "g"}
				resp.Skills = append(resp.Skills, skills.Skill{Path: "migrate", Name: "Migrate", Content: "migrate"})
			}
			body, _ := json.Marshal(resp)
			etag := skills.ComputeETag(body)
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// planEnv isolates HOME and a cache dir shared across the test's runs, and
// logs in as an account so skills fetches are authenticated.
func planEnv(t *testing.T) {
	t.Helper()
	isolateSkillsHome(t)
	t.Setenv("CIO_SKILLS_CACHE_DIR", t.TempDir())
	if err := client.WriteCredentials(&client.Credentials{
		ServiceAccountToken: "sa_live_plan",
		AccountID:           "1",
		Region:              "us",
	}); err != nil {
		t.Fatal(err)
	}
}

// runPlan runs `cio skills <args>` against srv, reusing the test's cache dir.
// --refresh is reset each time because rootCmd's flags persist between Execute calls.
func runPlan(t *testing.T, srv *planServer, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	out, errBuf := new(bytes.Buffer), new(bytes.Buffer)
	rootCmd.SetOut(out)
	rootCmd.SetErr(errBuf)
	rootCmd.SetArgs(append([]string{"skills", "--api-url", srv.URL, "--token", "", "--refresh=false"}, args...))
	err = rootCmd.Execute()
	return out.String(), errBuf.String(), err
}

// After a plan change, a plain run past the TTL (no --refresh) shows the newly
// enabled skill.
func TestSkillsPlanChangeShowsAfterTTLWithoutRefresh(t *testing.T) {
	planEnv(t)
	t.Setenv("CIO_SKILLS_TTL", "200ms")
	srv := newPlanServer(t)

	out, _, err := runPlan(t, srv, "list")
	if err != nil || strings.Contains(out, "migrate") {
		t.Fatalf("warm-up: err=%v out=%s", err, out)
	}

	srv.upgraded.Store(true)
	time.Sleep(300 * time.Millisecond)

	out, _, err = runPlan(t, srv, "read", "migrate")
	if err != nil {
		t.Fatalf("plain read after TTL should find the newly available skill: %v (%s)", err, out)
	}
}

// A failed token exchange falls back to the unauthenticated bundle, which lacks
// plan-gated skills; say so once rather than letting them read as unknown.
func TestSkillsWarnsWhenTokenExchangeFails(t *testing.T) {
	planEnv(t)
	srv := newPlanServer(t)
	srv.upgraded.Store(true)
	srv.exchangeStatus.Store(http.StatusInternalServerError)

	out, stderr, err := runPlan(t, srv, "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "migrate") {
		t.Fatalf("anonymous bundle unexpectedly carried a plan-gated skill: %s", out)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("expected a stderr warning that skills were fetched unauthenticated (account-gated skills omitted); stderr=%q", stderr)
	}

	_, stderr, _ = runPlan(t, srv, "read", "migrate")
	if n := strings.Count(stderr, "warning"); n != 1 {
		t.Errorf("read of an unknown skill should warn once, got %d: %q", n, stderr)
	}
}

// Every fetch or 304 restarts the TTL, so a skill or file enabled after the last
// run can be missing from a fresh cache; read revalidates before calling it unknown.
func TestSkillsReadUnknownRevalidatesWithinTTL(t *testing.T) {
	for _, arg := range []string{"migrate", "fly-api/gated.md"} {
		t.Run(arg, func(t *testing.T) {
			planEnv(t)
			srv := newPlanServer(t)

			if _, _, err := runPlan(t, srv, "list"); err != nil {
				t.Fatal(err)
			}
			srv.upgraded.Store(true)

			out, _, err := runPlan(t, srv, "read", arg)
			if err != nil {
				t.Errorf("read of %s missing from a fresh cache should revalidate once before failing: %v (%s)", arg, err, out)
			}
		})
	}
}

func TestSkillsReadKnownSkillDoesNotRevalidate(t *testing.T) {
	planEnv(t)
	srv := newPlanServer(t)

	if _, _, err := runPlan(t, srv, "list"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPlan(t, srv, "read", "fly-api/basics.md"); err != nil {
		t.Fatal(err)
	}
	if n := srv.skillFetches.Load(); n != 1 {
		t.Errorf("read of a cached skill within the TTL should not hit the server, got %d fetches", n)
	}
}

func TestSkillsReadUnknownAfterFetchDoesNotRevalidateAgain(t *testing.T) {
	planEnv(t)
	t.Setenv("CIO_SKILLS_TTL", "1ns")
	srv := newPlanServer(t)

	if _, _, err := runPlan(t, srv, "list"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPlan(t, srv, "read", "nope"); err == nil {
		t.Fatal("expected unknown skill")
	}
	if n := srv.skillFetches.Load(); n != 2 {
		t.Errorf("read that already fetched should not fetch again, got %d fetches", n)
	}
}
