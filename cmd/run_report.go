package cmd

import (
	"errors"
	"net/http"
	"strings"

	"github.com/customerio/cli/internal/client"
	"github.com/customerio/cli/internal/routes"
	"github.com/customerio/cli/internal/runreport"
	"github.com/customerio/cli/internal/validate"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func reportArgCounts(cmd *cobra.Command) {
	// These commands' Args validators check positional argument counts.
	if validate := cmd.Args; validate != nil {
		cmd.Args = func(cmd *cobra.Command, args []string) error {
			err := validate(cmd, args)
			if err != nil {
				runreport.Reason("arg_count")
			}
			return err
		}
	}
	for _, sub := range cmd.Commands() {
		reportArgCounts(sub)
	}
}

func reportBespokeRoutes(cmd *cobra.Command) {
	if !runreport.Enabled() || cmd == apiCmd {
		return
	}
	var idx *routes.PathIndex
	loaded := false
	runreport.SetRouteLookup(func(method, path string) (string, bool) {
		if !loaded {
			loaded = true
			var opts routes.LoadRegistryOptions
			if c := clientFromCmd(cmd); c != nil {
				opts = specCacheOptions(c)
			} else {
				tokenFlag, _ := cmd.Flags().GetString("token")
				opts.CacheKey = specCacheIdentity(client.ResolveServiceAccountToken(tokenFlag), client.ResolveAccessToken())
			}
			idx, _ = routes.LoadPathIndexFromCache(opts)
		}
		if idx == nil {
			return "", false
		}
		return idx.Lookup(method, path)
	})
}

func reportCommandFailure(cmd *cobra.Command, err error) {
	if err == nil {
		return
	}
	var flagErr *pflag.NotExistError
	var paramsErr *validate.ParamsValidationError
	var jsonErr *validate.JSONValidationError
	var apiErr *client.APIError
	var nonJSON *client.NonJSONResponseError
	reason := runreport.FailureReason(err)
	switch {
	case reason != "":
		runreport.Reason(reason)
	case cmd == rootCmd && cmd.CalledAs() == "":
		// Cobra failed to resolve the command before its validators could run.
		runreport.Reason("unknown_command")
	case errors.As(err, &flagErr):
		runreport.Reason("unknown_flag")
	case errors.As(err, &paramsErr):
		runreport.Reason("invalid_params")
	case errors.As(err, &jsonErr):
		runreport.Reason("invalid_json")
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized:
		runreport.Reason("auth")
	case errors.As(err, &apiErr):
		runreport.Reason("http_error")
	case errors.As(err, &nonJSON) && (nonJSON.StatusCode < http.StatusOK || nonJSON.StatusCode >= http.StatusMultipleChoices):
		runreport.Reason("http_error")
	}
}

func commandPathBelowRoot(cmd *cobra.Command) string {
	if cmd == rootCmd {
		return ""
	}
	return strings.TrimPrefix(cmd.CommandPath(), rootCmd.CommandPath()+" ")
}
