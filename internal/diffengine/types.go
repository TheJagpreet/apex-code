// Package diffengine applies compact anchor patches safely and verifies the
// affected files with token-friendly failure summaries.
package diffengine

import "context"

const PatchFormat = `anchor patch JSON:
{
  "files": [
    {
      "path": "relative/or/absolute/file",
      "expected_sha256": "optional current file hash",
      "hunks": [
        {
          "old": "exact block to replace",
          "new": "replacement block",
          "anchor_before": "optional nearby text before old",
          "anchor_after": "optional nearby text after old"
        }
      ]
    }
  ],
  "verify_commands": ["optional command after apply"]
}`

type Patch struct {
	Files          []FilePatch `json:"files"`
	VerifyCommands []string    `json:"verify_commands,omitempty"`
	Format         bool        `json:"format,omitempty"`
}

type FilePatch struct {
	Path           string `json:"path"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Hunks          []Hunk `json:"hunks"`
}

type Hunk struct {
	Old          string `json:"old"`
	New          string `json:"new"`
	AnchorBefore string `json:"anchor_before,omitempty"`
	AnchorAfter  string `json:"anchor_after,omitempty"`
}

type Result struct {
	AppliedFiles []string
	HunksApplied int
	RolledBack   bool
	Verification VerificationResult
}

type VerificationResult struct {
	OK       bool
	ExitCode int
	Output   string
	Command  string
}

type Options struct {
	Root           string
	Format         bool
	VerifyHooks    []string
	OutputMaxLines int
	OutputMaxChars int
}

type Engine struct {
	opts Options
}

type HookRunner interface {
	Run(ctx context.Context, command string, root string) VerificationResult
}
