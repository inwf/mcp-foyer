package semantic_test

import (
	"context"
	"strings"
	"testing"

	"mcp-foyer/internal/config"
)

// A model trained with markers on its inputs ("query: ", "passage: ")
// has to receive them on both sides: a query embedded without its marker
// lands somewhere the tool texts were never placed. What reaches the
// service is what decides the vectors, so that is what is asserted.
func TestThePrefixesReachTheService(t *testing.T) {
	service := startVectorService(t)
	ix := indexWith(t, service, func(e *config.Embedding) {
		e.QueryPrefix = "query: "
		e.DocumentPrefix = "passage: "
	})
	_, built := service.counts()

	ix.Similarities(context.Background(), "alpha", alphaCandidates())
	_, all := service.counts()

	if len(built) == 0 {
		t.Fatal("the index build sent no tool texts")
	}
	for _, text := range built {
		if !strings.HasPrefix(text, "passage: ") {
			t.Errorf("tool text %q was embedded without the document prefix", text)
		}
	}
	queries := all[len(built):]
	if len(queries) != 1 || queries[0] != "query: alpha" {
		t.Errorf("queries sent = %q, want [\"query: alpha\"]", queries)
	}
}

// Changing the document prefix changes every tool's text; vectors made
// from the old text are answers to a different question and have to be
// made again. Without this a prefix set after the first build would only
// ever apply to the queries.
func TestChangingTheDocumentPrefixRebuildsTheIndex(t *testing.T) {
	service := startVectorService(t)
	m := managerFor(t, service.URL, nil)
	ix := newIndexer(t, alphaSources(), m)

	if _, err := ix.Rebuild(context.Background()); err != nil {
		t.Fatalf("first Rebuild: %v", err)
	}
	updateManager(t, m, func(c *config.Config) { c.Embedding.DocumentPrefix = "passage: " })
	result, err := ix.Rebuild(context.Background())
	if err != nil {
		t.Fatalf("second Rebuild: %v", err)
	}

	if result.Embedded != result.Total || result.Total == 0 {
		t.Errorf("the second round embedded %d of %d tools, want every one", result.Embedded, result.Total)
	}
}

// The query cache is keyed on what was sent. A vector cached under one
// query prefix is not the answer once the prefix has changed.
func TestChangingTheQueryPrefixAsksTheServiceAgain(t *testing.T) {
	service := startVectorService(t)
	m := managerFor(t, service.URL, nil)
	ix := newIndexer(t, alphaSources(), m)
	if _, err := ix.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	ix.Similarities(context.Background(), "alpha", alphaCandidates())
	updateManager(t, m, func(c *config.Config) { c.Embedding.QueryPrefix = "query: " })
	ix.Similarities(context.Background(), "alpha", alphaCandidates())

	_, inputs := service.counts()
	if last := inputs[len(inputs)-1]; last != "query: alpha" {
		t.Errorf("the last text sent was %q, want the query under its new prefix", last)
	}
}
