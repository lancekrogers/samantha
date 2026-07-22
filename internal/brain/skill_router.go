package brain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/skills"
)

const (
	defaultSemanticSkillMinScore = 0.55
	semanticSkillTopBand         = 0.06
	semanticSkillMaxMatch        = 3
)

type skillEmbedder interface {
	Embed(context.Context, *api.EmbedRequest) (*api.EmbedResponse, error)
}

// semanticSkillRouter caches embeddings for the Tier 1 skill catalog and
// matches each user prompt to that catalog. Only selected Tier 2 instructions
// are injected, preserving Agent Skills progressive disclosure.
type semanticSkillRouter struct {
	embedder skillEmbedder
	model    string
	catalog  []skills.Skill
	vectors  [][]float32
	minScore float64
}

func newSemanticSkillRouter(ctx context.Context, embedder skillEmbedder, model string, minScore float64, catalog []skills.Skill) (*semanticSkillRouter, error) {
	model = strings.TrimSpace(model)
	if embedder == nil || model == "" || len(catalog) == 0 {
		return nil, nil
	}

	inputs := make([]string, len(catalog))
	for i, skill := range catalog {
		inputs[i] = skillRoutingText(skill)
	}
	resp, err := embedder.Embed(ctx, &api.EmbedRequest{Model: model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("embedding skill catalog with model %q: %w", model, err)
	}
	if len(resp.Embeddings) != len(catalog) {
		return nil, fmt.Errorf("embedding skill catalog with model %q: got %d vectors for %d skills", model, len(resp.Embeddings), len(catalog))
	}
	for i, vector := range resp.Embeddings {
		if len(vector) == 0 {
			return nil, fmt.Errorf("embedding skill catalog with model %q: skill %q returned an empty vector", model, catalog[i].Name)
		}
	}

	return &semanticSkillRouter{
		embedder: embedder,
		model:    model,
		catalog:  catalog,
		vectors:  resp.Embeddings,
		minScore: semanticSkillThreshold(minScore),
	}, nil
}

func semanticSkillThreshold(value float64) float64 {
	if value <= 0 {
		return defaultSemanticSkillMinScore
	}
	return min(value, 1)
}

func skillRoutingText(skill skills.Skill) string {
	return strings.TrimSpace(skill.Name) + ": " + strings.TrimSpace(skill.Description)
}

type scoredSkill struct {
	index int
	score float64
}

func (r *semanticSkillRouter) Match(ctx context.Context, prompt string) ([]skills.Skill, error) {
	if r == nil || strings.TrimSpace(prompt) == "" {
		return nil, nil
	}
	resp, err := r.embedder.Embed(ctx, &api.EmbedRequest{Model: r.model, Input: prompt})
	if err != nil {
		return nil, fmt.Errorf("embedding user prompt with model %q: %w", r.model, err)
	}
	if len(resp.Embeddings) != 1 || len(resp.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("embedding user prompt with model %q: expected one non-empty vector", r.model)
	}

	query := resp.Embeddings[0]
	scores := make([]scoredSkill, 0, len(r.catalog))
	for i, vector := range r.vectors {
		score := cosineSimilarity(query, vector)
		if score >= r.minScore {
			scores = append(scores, scoredSkill{index: i, score: score})
		}
	}
	if len(scores) == 0 {
		return nil, nil
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].score > scores[j].score })

	top := scores[0].score
	matches := make([]skills.Skill, 0, min(len(scores), semanticSkillMaxMatch))
	for _, candidate := range scores {
		if len(matches) >= semanticSkillMaxMatch || candidate.score < top-semanticSkillTopBand {
			break
		}
		matches = append(matches, r.catalog[candidate.index])
	}
	return matches, nil
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return -1
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ActivatedSkillContext wraps automatically selected skill bodies separately
// from the static catalog so the model can distinguish discovered metadata
// from instructions activated for the current user prompt.
func ActivatedSkillContext(matched []skills.Skill) string {
	if len(matched) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n<activated_skills>\n")
	b.WriteString("The harness selected these Agent Skills for the current user request. Follow their instructions. Relative paths are rooted at each skill directory.\n")
	for _, skill := range matched {
		body := strings.TrimSpace(skill.Body)
		if len(body) > skillBodyMaxBytes {
			body = body[:skillBodyMaxBytes] + "\n... (truncated)"
		}
		fmt.Fprintf(&b, "\n<skill name=%q directory=%q>\n%s\n</skill>\n", skill.Name, skill.Dir, body)
	}
	b.WriteString("</activated_skills>\n")
	return b.String()
}
