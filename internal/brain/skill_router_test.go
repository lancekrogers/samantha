package brain

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/skills"
)

type routingEmbedder struct {
	catalog [][]float32
	queries map[string][]float32
	err     error
}

func (e *routingEmbedder) Embed(_ context.Context, req *api.EmbedRequest) (*api.EmbedResponse, error) {
	if e.err != nil {
		return nil, e.err
	}
	switch input := req.Input.(type) {
	case []string:
		return &api.EmbedResponse{Embeddings: e.catalog}, nil
	case string:
		vector, ok := e.queries[input]
		if !ok {
			return nil, fmt.Errorf("unexpected query %q", input)
		}
		return &api.EmbedResponse{Embeddings: [][]float32{vector}}, nil
	default:
		return nil, fmt.Errorf("unexpected input type %T", input)
	}
}

func routingCatalog() []skills.Skill {
	return []skills.Skill{
		{Name: "campaign-navigation", Description: "Move between campaign projects and festivals", Body: "Use camp go.", Dir: "/skills/nav"},
		{Name: "pdf", Description: "Read and create PDF documents", Body: "Render the PDF.", Dir: "/skills/pdf"},
		{Name: "nearby", Description: "Related campaign workflow", Body: "Inspect workflow state.", Dir: "/skills/nearby"},
	}
}

func TestSemanticSkillRouterMatchesPromptAndPreservesProgressiveDisclosure(t *testing.T) {
	t.Parallel()
	embedder := &routingEmbedder{
		catalog: [][]float32{{1, 0}, {0, 1}, {0.998, 0.063}},
		queries: map[string][]float32{
			"take me to another campaign project": {1, 0},
			"how are you":                         {-1, 0},
		},
	}
	router, err := newSemanticSkillRouter(context.Background(), embedder, "embed", 0.55, routingCatalog())
	if err != nil {
		t.Fatal(err)
	}

	matched, err := router.Match(context.Background(), "take me to another campaign project")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 2 || matched[0].Name != "campaign-navigation" || matched[1].Name != "nearby" {
		t.Fatalf("matches = %#v", matched)
	}
	activated := ActivatedSkillContext(matched)
	if !strings.Contains(activated, "Use camp go.") || !strings.Contains(activated, `directory="/skills/nav"`) {
		t.Fatalf("activated context = %q", activated)
	}
	if strings.Contains(SkillContext(routingCatalog()), "Use camp go.") {
		t.Fatal("Tier 1 catalog eagerly included a Tier 2 skill body")
	}

	unrelated, err := router.Match(context.Background(), "how are you")
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated matches = %#v", unrelated)
	}
}

func TestRouteSkillContextReportsAutomaticActivation(t *testing.T) {
	t.Parallel()
	embedder := &routingEmbedder{
		catalog: [][]float32{{1, 0}, {0, 1}, {0, 1}},
		queries: map[string][]float32{"navigate": {1, 0}},
	}
	router, err := newSemanticSkillRouter(context.Background(), embedder, "embed", 0.55, routingCatalog())
	if err != nil {
		t.Fatal(err)
	}
	o := &OllamaBrain{skillRouter: router}
	var started, ended string
	got := o.routeSkillContext(context.Background(), "navigate",
		func(name, detail string) { started = name + ":" + detail },
		func(name, detail string) { ended = name + ":" + detail },
	)
	if !strings.Contains(got, "Use camp go.") {
		t.Fatalf("context = %q", got)
	}
	if started != "activate_skill:campaign-navigation" || !strings.Contains(ended, "injected 1 skill(s): campaign-navigation") {
		t.Fatalf("hooks = %q / %q", started, ended)
	}
}

func TestSemanticSkillRouterFailsOpenWhenUnavailable(t *testing.T) {
	t.Parallel()
	_, err := newSemanticSkillRouter(context.Background(), &routingEmbedder{err: fmt.Errorf("model missing")}, "embed", 0.55, routingCatalog())
	if err == nil || !strings.Contains(err.Error(), "model missing") {
		t.Fatalf("error = %v", err)
	}
	if got, err := (*semanticSkillRouter)(nil).Match(context.Background(), "anything"); err != nil || got != nil {
		t.Fatalf("nil router = %#v, %v", got, err)
	}
}

func TestSemanticSkillThreshold(t *testing.T) {
	t.Parallel()
	if got := semanticSkillThreshold(0); got != defaultSemanticSkillMinScore {
		t.Fatalf("zero threshold = %v", got)
	}
	if got := semanticSkillThreshold(2); got != 1 {
		t.Fatalf("high threshold = %v", got)
	}
}

func TestCosineSimilarity(t *testing.T) {
	t.Parallel()
	if got := cosineSimilarity([]float32{1, 0}, []float32{1, 0}); math.Abs(got-1) > 1e-9 {
		t.Fatalf("same vector score = %v", got)
	}
	if got := cosineSimilarity([]float32{1, 0}, []float32{0, 1}); math.Abs(got) > 1e-9 {
		t.Fatalf("orthogonal vector score = %v", got)
	}
}

func TestSemanticSkillRouterWithOllama(t *testing.T) {
	if os.Getenv("SAMANTHA_TEST_OLLAMA_EMBEDDINGS") == "" {
		t.Skip("set SAMANTHA_TEST_OLLAMA_EMBEDDINGS=1 to run against local Ollama")
	}
	base, err := url.Parse("http://localhost:11434")
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("OLLAMA_EMBEDDING_MODEL")
	if model == "" {
		model = "nomic-embed-text"
	}
	router, err := newSemanticSkillRouter(context.Background(), api.NewClient(base, http.DefaultClient), model, 0.55, routingCatalog())
	if err != nil {
		t.Fatal(err)
	}
	matched, err := router.Match(context.Background(), "take me to another project in this campaign")
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) == 0 || matched[0].Name != "campaign-navigation" {
		t.Fatalf("matches = %#v, want campaign-navigation first", matched)
	}
}
