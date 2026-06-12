package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/customerio/cli/internal/output"
	"github.com/customerio/cli/internal/skills"
	"github.com/spf13/cobra"
)

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "List available agent skills",
	Long: `List and read agent skills downloaded from the Customer.io backend.

Skills are task-specific instruction manuals for AI agents. They contain
routing rules, required parameters, multi-step workflows, and gotchas
that are not obvious from the API alone.

  cio skills                          — list all available skills
  cio skills list                     — same as above
  cio skills read <path>              — read a skill's main content
  cio skills read <path>/<file>       — read a skill sub-file
  cio skills prompt                   — output the routing rules for agent context`,
	Args: cobra.NoArgs,
	RunE: runSkillsList,
}

var skillsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available agent skills",
	Args:  cobra.NoArgs,
	RunE:  runSkillsList,
}

var skillsReadCmd = &cobra.Command{
	Use:   "read <skill-path>",
	Short: "Read a skill's content",
	Long: `Read the content of a specific skill or sub-file.

  cio skills read fly-api                — read the main SKILL.md content
  cio skills read fly-api/campaigns.md   — read a sub-file`,
	Args: cobra.ExactArgs(1),
	RunE: runSkillsRead,
}

var skillsPromptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Output the skills routing rules for agent context injection",
	Args:  cobra.NoArgs,
	RunE:  runSkillsPrompt,
}

func init() {
	skillsCmd.PersistentFlags().Bool("refresh", false, "Force re-download of skills")
	skillsCmd.AddCommand(skillsListCmd)
	skillsCmd.AddCommand(skillsReadCmd)
	skillsCmd.AddCommand(skillsPromptCmd)
	rootCmd.AddCommand(skillsCmd)
}

func loadSkills(cmd *cobra.Command) (*skills.SkillsResponse, error) {
	refresh, _ := cmd.Flags().GetBool("refresh")

	var baseURL string
	if c := clientFromCmd(cmd); c != nil {
		baseURL = c.BaseURL()
	}

	resp, err := skills.EnsureSkills(cmd.Context(), skills.LoadOptions{
		BaseURL:      baseURL,
		ForceRefresh: refresh,
	})
	if err != nil {
		output.PrintError(output.CodeGeneralError, fmt.Sprintf("failed to load skills: %v", err), nil)
		return nil, err
	}
	for _, notice := range resp.Notices {
		fmt.Fprintln(cmd.ErrOrStderr(), "cio: "+notice)
	}
	return resp, nil
}

func runSkillsList(cmd *cobra.Command, args []string) error {
	resp, err := loadSkills(cmd)
	if err != nil {
		return err
	}

	type fileSummary struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}

	type skillSummary struct {
		Path        string        `json:"path"`
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Files       []fileSummary `json:"files,omitempty"`
	}

	var result []skillSummary
	for _, s := range resp.Skills {
		files := make([]fileSummary, 0, len(s.Files))
		for _, name := range s.SortedFiles() {
			files = append(files, fileSummary{
				Name:        name,
				Description: skills.FrontmatterDescription(s.Files[name]),
			})
		}
		result = append(result, skillSummary{
			Path:        s.Path,
			Name:        s.Name,
			Description: s.Description,
			Files:       files,
		})
	}

	return skillsOutput(cmd, result)
}

func runSkillsRead(cmd *cobra.Command, args []string) error {
	resp, err := loadSkills(cmd)
	if err != nil {
		return err
	}

	path := args[0]

	// Split into skill path and optional sub-file.
	skillPath, subFile := splitSkillArg(path)

	for _, s := range resp.Skills {
		if s.Path != skillPath {
			continue
		}

		if subFile == "" {
			// Return the skill's routing index: authored SKILL.md content if
			// present, otherwise an index synthesized from the sub-files'
			// frontmatter descriptions.
			return skillsOutput(cmd, map[string]any{
				"path":    s.Path,
				"name":    s.Name,
				"content": s.Index(),
			})
		}

		// Look up the sub-file.
		content, ok := s.Files[subFile]
		if !ok {
			available := make([]string, 0, len(s.Files))
			for f := range s.Files {
				available = append(available, f)
			}
			err := fmt.Errorf("unknown file %q in skill %q", subFile, skillPath)
			output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
				"skill":           skillPath,
				"available_files": available,
			})
			return err
		}

		return skillsOutput(cmd, map[string]any{
			"path":    path,
			"name":    s.Name,
			"content": content,
		})
	}

	// Skill not found.
	available := make([]string, 0, len(resp.Skills))
	for _, s := range resp.Skills {
		available = append(available, s.Path)
	}
	err = fmt.Errorf("unknown skill: %s", skillPath)
	output.PrintError(output.CodeValidationError, err.Error(), map[string]any{
		"available_skills": available,
	})
	return err
}

func runSkillsPrompt(cmd *cobra.Command, args []string) error {
	resp, err := loadSkills(cmd)
	if err != nil {
		return err
	}

	return skillsOutput(cmd, map[string]string{
		"prompt": resp.Prompt,
	})
}

func splitSkillArg(path string) (skillPath, subFile string) {
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

func skillsOutput(cmd *cobra.Command, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	jq := GetJQFlag(cmd)
	return output.FprintProcess(cmd.OutOrStdout(), json.RawMessage(data), jq)
}
