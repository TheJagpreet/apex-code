// Package promptasm assembles provider requests from ordered, cache-aware
// prompt sections.
package promptasm

import (
	"context"

	"github.com/apex-code/apex/internal/domain"
)

type SectionName string

const (
	SectionSystem      SectionName = "system"
	SectionTools       SectionName = "tools"
	SectionRepoMap     SectionName = "repo-map"
	SectionHistory     SectionName = "history"
	SectionWorkingFile SectionName = "working-files"
	SectionLatestUser  SectionName = "latest-user"
	SectionFreshTool   SectionName = "fresh-tool-output"
)

type Section struct {
	Name     SectionName
	Messages []domain.Message
	Text     string
}

type Input struct {
	Model        string
	System       []domain.Message
	Tools        []domain.ToolSpec
	RepoMap      string
	History      []domain.Message
	WorkingFiles []string
	LatestUser   domain.Message
	FreshTool    []domain.Message
	Temperature  float64
	MaxTokens    int
	Stop         []string
	KeepAlive    string
	PromptCache  bool
}

type Output struct {
	Request          domain.Request
	StablePrefixHash string
	StablePrefix     []domain.Message
}

type Assembler struct {
	ToolDescriptorLimit int
	DefaultKeepAlive    string
}

func New() *Assembler {
	return &Assembler{
		ToolDescriptorLimit: 80,
		DefaultKeepAlive:    "10m",
	}
}

type CacheStore interface {
	Get(ctx context.Context, key string) (CacheEntry, bool, error)
	Put(ctx context.Context, entry CacheEntry) error
}

type CacheEntry struct {
	Key       string
	Kind      string
	Value     string
	Hash      string
	CreatedAt int64
}
