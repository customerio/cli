package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/output"
)

func TestHandleAPIError_Forbidden(t *testing.T) {
	cases := []struct {
		desc        string
		body        string
		wantMessage string
		wantAPICode string
	}{
		{
			desc:        "given a coded 403 body, handleAPIError should pass on the detail and code",
			body:        `{"errors":[{"code":"update_requires_edit_permission","detail":"Updating this one-time send requires the Edit permission.","status":"403"}]}`,
			wantMessage: "Updating this one-time send requires the Edit permission.",
			wantAPICode: "update_requires_edit_permission",
		},
		{
			desc:        "given a 403 body without a code, handleAPIError should pass on the detail alone",
			body:        `{"errors":[{"detail":"Only account admins can do this.","status":"403"}]}`,
			wantMessage: "Only account admins can do this.",
		},
		{
			desc:        "given a 403 body whose detail is a bare forbidden, handleAPIError should say permissions are insufficient",
			body:        `{"errors":[{"detail":" Forbidden ","status":"403"}]}`,
			wantMessage: "Insufficient permissions.",
		},
		{
			desc:        "given a coded 403 body whose detail is a bare forbidden, handleAPIError should say permissions are insufficient and pass on the code",
			body:        `{"errors":[{"code":"missing_permission","detail":"forbidden","status":"403"}]}`,
			wantMessage: "Insufficient permissions.",
			wantAPICode: "missing_permission",
		},
		{
			desc:        "given an empty 403 body, handleAPIError should say permissions are insufficient",
			wantMessage: "Insufficient permissions.",
		},
		{
			desc:        "given a non-JSON 403 body, handleAPIError should say permissions are insufficient",
			body:        `<html>Forbidden</html>`,
			wantMessage: "Insufficient permissions.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			stderr := captureStderr(t, func() {
				_ = handleAPIError(&client.APIError{StatusCode: http.StatusForbidden, Body: []byte(tc.body)})
			})

			var got struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details struct {
					StatusCode int    `json:"status_code"`
					APICode    string `json:"api_code"`
				} `json:"details"`
			}
			if err := json.Unmarshal([]byte(stderr), &got); err != nil {
				t.Fatalf("stderr should be a JSON error: %v\n%s", err, stderr)
			}
			if got.Code != output.CodeAuthzError {
				t.Errorf("code = %q, want %q", got.Code, output.CodeAuthzError)
			}
			if got.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", got.Message, tc.wantMessage)
			}
			if got.Details.StatusCode != http.StatusForbidden {
				t.Errorf("status_code = %d, want %d", got.Details.StatusCode, http.StatusForbidden)
			}
			if got.Details.APICode != tc.wantAPICode {
				t.Errorf("api_code = %q, want %q", got.Details.APICode, tc.wantAPICode)
			}
		})
	}
}

func TestAPIErrorDetail(t *testing.T) {
	cases := []struct {
		desc       string
		body       string
		wantCode   string
		wantDetail string
	}{
		{
			desc:       "given a string code and a detail, apiErrorDetail should return both",
			body:       `{"errors":[{"code":"update_requires_edit_permission","detail":"Needs Edit."}]}`,
			wantCode:   "update_requires_edit_permission",
			wantDetail: "Needs Edit.",
		},
		{
			desc:       "given a numeric code and a detail, apiErrorDetail should return the detail without a code",
			body:       `{"errors":[{"code":400,"detail":"Add funds is only available on the Flex plan."}]}`,
			wantDetail: "Add funds is only available on the Flex plan.",
		},
		{
			desc: "given a non-JSON body, apiErrorDetail should return nothing",
			body: `<html>Bad Request</html>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			code, detail := apiErrorDetail([]byte(tc.body))
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}
