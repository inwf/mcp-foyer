package gateway_test

import (
	"context"
	"flag"
	"testing"

	"mcp-foyer/internal/config"
	"mcp-foyer/internal/embedding"
	"mcp-foyer/internal/gateway"
)

// The corpus and every ranking query, embedded once by a real model and
// kept in testdata, so that the fused ranking can be measured on the same
// rows as the lexical one without a service in the loop. The file is the
// embedding index's own format; queries are stored under a server name no
// corpus server uses.
const (
	corpusVectorsPath = "testdata/search_vectors.bin"
	queryServer       = "\x00query"
)

var embedConfig = flag.String("embed-config", "",
	"a configuration whose embedding section regenerates "+corpusVectorsPath)

// Regenerating is opt-in: it calls the service the configuration names.
//
//	go test ./internal/gateway -run TestRegenerateCorpusVectors -args -embed-config=../data/config.yaml
func TestRegenerateCorpusVectors(t *testing.T) {
	if *embedConfig == "" {
		t.Skip("set -args -embed-config=path/to/config.yaml to regenerate the corpus vectors")
	}
	cfg, err := config.Load(*embedConfig)
	if err != nil {
		t.Fatal(err)
	}
	section := cfg.Embedding

	var docs []embedding.Document
	for _, c := range corpusCandidates(t) {
		docs = append(docs, embedding.Document{
			Server: c.Server, Tool: c.Tool,
			Text: section.DocumentPrefix + gateway.EmbeddingText(c),
		})
	}
	for _, tc := range rankingQueries {
		docs = append(docs, embedding.Document{
			Server: queryServer, Tool: tc.query, Text: section.QueryPrefix + tc.query,
		})
	}

	result, err := embedding.NewIndex(corpusVectorsPath).Sync(context.Background(), section.Model, docs, embedding.New(section))
	if err != nil {
		t.Fatal(err)
	}
	if result.Indexed != len(docs) {
		t.Fatalf("indexed %d of %d texts", result.Indexed, len(docs))
	}
	t.Logf("embedded %d texts with %s", result.Indexed, section.Model)
}

// corpusSemantics answers similarities from the stored vectors, with the
// floor a deployment uses by default.
type corpusSemantics struct {
	index *embedding.Index
	floor float32
}

func (s corpusSemantics) Similarities(_ context.Context, query string, candidates []gateway.Searchable) []float32 {
	q, ok := s.index.Vector(embedding.Key{Server: queryServer, Tool: query})
	if !ok {
		return nil
	}
	out := make([]float32, len(candidates))
	for i, c := range candidates {
		v, ok := s.index.Vector(embedding.Key{Server: c.Server, Tool: c.Tool})
		if !ok {
			continue
		}
		if sim := embedding.Cosine(q, v); sim > 0 && sim >= s.floor {
			out[i] = sim
		}
	}
	return out
}

// The number the semantic side exists to move: of the rows the lexical
// ranking puts first, how many the fused ranking also puts first, and
// how many rows the fusion gets right that the words alone do not. It
// is reported rather than asserted row by row: the vectors are one
// model's opinion, and a row that one model ranks differently is a fact
// about that model.
//
// What is asserted is that the fusion does not lose ground overall.
// Without that, a change to the fusion that made search worse with every
// model would pass here as silently as one that made it better.
func TestFusionWithARealModelDoesNotRankWorseThanTheWords(t *testing.T) {
	index, err := embedding.OpenIndex(corpusVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	if index.Width() == 0 {
		t.Fatalf("%s holds no vectors; regenerate it (see TestRegenerateCorpusVectors)", corpusVectorsPath)
	}
	sem := corpusSemantics{index: index, floor: float32(config.Default().Embedding.MinSimilarity)}
	candidates := corpusCandidates(t)

	lexical, fused := 0, 0
	for _, tc := range rankingQueries {
		words := gateway.SearchTools(tc.query, candidates, 1)
		both, _ := gateway.RankTools(context.Background(), tc.query, candidates, 1, sem)
		wordsRight := len(words) == 1 && ranksAmongTopThree(words, tc.want)
		bothRight := len(both) == 1 && ranksAmongTopThree(both, tc.want)
		if wordsRight {
			lexical++
		}
		if bothRight {
			fused++
		}
		if wordsRight != bothRight {
			t.Logf("%q: words %v, fused %v, want %v", tc.query, originNames(words), originNames(both), tc.want)
		}
	}
	t.Logf("%s: top-1 %d/%d by words, %d/%d fused", index.Status().Model,
		lexical, len(rankingQueries), fused, len(rankingQueries))
	if fused < lexical {
		t.Errorf("the fused ranking put the intended tool first %d times, the words alone %d", fused, lexical)
	}
}
