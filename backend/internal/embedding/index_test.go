package embedding_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-foyer/internal/embedding"
)

// fakeEmbedder answers deterministic vectors and remembers what it was
// asked for, so that what is under test is the index rather than HTTP.
type fakeEmbedder struct {
	calls   int
	batches []int
	failAt  int // the 1-based call that fails; zero for none
	width   int
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.batches = append(f.batches, len(texts))
	if f.failAt == f.calls {
		return nil, errors.New("the service is down")
	}

	width := f.width
	if width == 0 {
		width = 3
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vector := make([]float32, width)
		for j := range vector {
			vector[j] = float32(len(text)+j) / 10
		}
		out[i] = vector
	}
	return out, nil
}

type embedderFunc func(context.Context, []string) ([][]float32, error)

func (f embedderFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

type emptyVectorEmbedder struct{}

func (emptyVectorEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{}
	}
	return out, nil
}

func docsOf(names ...string) []embedding.Document {
	out := make([]embedding.Document, len(names))
	for i, name := range names {
		out[i] = embedding.Document{Server: "files", Tool: name, Text: "read a " + name}
	}
	return out
}

func newAt(t *testing.T) (*embedding.Index, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "embeddings.bin")
	return embedding.NewIndex(path), path
}

func TestAMissingIndexIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embeddings.bin")

	ix, err := embedding.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	if got := ix.Status().Indexed; got != 0 {
		t.Errorf("indexed = %d, want 0", got)
	}
}

func TestARoundIndexesEveryDocumentAndARepeatedRoundEmbedsNothing(t *testing.T) {
	ix, path := newAt(t)
	docs := docsOf("read", "write", "list")

	result, err := ix.Sync(context.Background(), "bge-m3", docs, &fakeEmbedder{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Indexed != 3 || result.Embedded != 3 {
		t.Errorf("result = %+v, want three indexed and three embedded", result)
	}

	// A restart rereads the same vectors, so the next round has nothing
	// to do — which is the whole point of the file.
	reopened, err := embedding.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	again := &fakeEmbedder{}
	result, err = reopened.Sync(context.Background(), "bge-m3", docs, again)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if again.calls != 0 {
		t.Errorf("the service was called %d times, want none", again.calls)
	}
	if result.Indexed != 3 || result.Embedded != 0 {
		t.Errorf("result = %+v, want three indexed and nothing embedded", result)
	}
}

func TestTheVectorsSurviveARestart(t *testing.T) {
	ix, path := newAt(t)
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), &fakeEmbedder{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	before, ok := ix.Vector(embedding.Key{Server: "files", Tool: "read"})
	if !ok {
		t.Fatal("the tool has no vector after a round")
	}

	reopened, err := embedding.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	after, ok := reopened.Vector(embedding.Key{Server: "files", Tool: "read"})
	if !ok {
		t.Fatal("the tool has no vector after reopening")
	}
	if len(before) != len(after) {
		t.Fatalf("the width changed across a restart: %d then %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("vector[%d] = %v after reopening, was %v", i, after[i], before[i])
		}
	}
}

func TestAChangedTextIsEmbeddedAgain(t *testing.T) {
	ix, _ := newAt(t)
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), &fakeEmbedder{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	changed := []embedding.Document{{Server: "files", Tool: "read", Text: "read a file, now with more words"}}
	result, err := ix.Sync(context.Background(), "bge-m3", changed, &fakeEmbedder{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Embedded != 1 {
		t.Errorf("embedded %d, want the changed text embedded again", result.Embedded)
	}
}

// The model is part of what a vector means: the same text asked of
// another model is a different question.
func TestAChangedModelIsEmbeddedAgain(t *testing.T) {
	ix, _ := newAt(t)
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), &fakeEmbedder{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	result, err := ix.Sync(context.Background(), "another-model", docsOf("read"), &fakeEmbedder{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Embedded != 1 {
		t.Errorf("embedded %d, want the text embedded again for the new model", result.Embedded)
	}
}

func TestToolsThatAreGoneLeaveTheIndex(t *testing.T) {
	ix, _ := newAt(t)
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read", "write"), &fakeEmbedder{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	result, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), &fakeEmbedder{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Indexed != 1 {
		t.Errorf("indexed = %d, want the removed tool gone", result.Indexed)
	}
	if _, ok := ix.Vector(embedding.Key{Server: "files", Tool: "write"}); ok {
		t.Error("the removed tool still has a vector")
	}
}

// One request has a bounded size whatever the directory holds.
func TestTextsGoOutInBatches(t *testing.T) {
	ix, _ := newAt(t)
	names := make([]string, 70)
	for i := range names {
		names[i] = fmt.Sprintf("tool-%d", i)
	}

	embedder := &fakeEmbedder{}
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf(names...), embedder); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	want := []int{32, 32, 6}
	if len(embedder.batches) != len(want) {
		t.Fatalf("batches = %v, want %v", embedder.batches, want)
	}
	for i, size := range want {
		if embedder.batches[i] != size {
			t.Errorf("batch %d carried %d texts, want %d", i+1, embedder.batches[i], size)
		}
	}
}

// A batch that fails must not throw away the batches that worked: the
// next round picks up where this one stopped.
func TestAFailedBatchKeepsWhatWasFinished(t *testing.T) {
	ix, _ := newAt(t)
	names := make([]string, 40)
	for i := range names {
		names[i] = fmt.Sprintf("tool-%d", i)
	}
	docs := docsOf(names...)

	result, err := ix.Sync(context.Background(), "bge-m3", docs, &fakeEmbedder{failAt: 2})
	if err == nil {
		t.Fatal("Sync succeeded against a failing service")
	}
	if result.Indexed != 32 {
		t.Errorf("indexed = %d, want the first batch kept", result.Indexed)
	}
	if status := ix.Status(); !strings.Contains(status.LastError, "the service is down") {
		t.Errorf("lastError = %q, want the failure", status.LastError)
	}

	result, err = ix.Sync(context.Background(), "bge-m3", docs, &fakeEmbedder{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Embedded != 8 {
		t.Errorf("embedded %d on the second round, want only the missing eight", result.Embedded)
	}
	if result.Indexed != 40 {
		t.Errorf("indexed = %d, want all forty", result.Indexed)
	}
	if status := ix.Status(); status.LastError != "" {
		t.Errorf("lastError = %q, want it cleared by a round that finished", status.LastError)
	}
}

// A service answering at a different width under the same model name is
// not answering with the model the index was built by. Nothing indexed
// can be trusted alongside the new vectors, so all of it is dropped.
func TestAWidthChangeClearsTheIndex(t *testing.T) {
	ix, path := newAt(t)
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), &fakeEmbedder{width: 3}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	_, err := ix.Sync(context.Background(), "bge-m3", docsOf("read", "write"), &fakeEmbedder{width: 5})
	if err == nil {
		t.Fatal("Sync accepted vectors of a different width")
	}
	if !strings.Contains(err.Error(), "width") {
		t.Errorf("error %q does not name the problem", err)
	}
	if got := ix.Status().Indexed; got != 0 {
		t.Errorf("indexed = %d, want the index cleared", got)
	}

	// And the file agrees, so a restart does not read the old width back.
	reopened, err := embedding.OpenIndex(path)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	if got := reopened.Status().Indexed; got != 0 {
		t.Errorf("indexed = %d after reopening, want 0", got)
	}
}

func TestAnEmptyVectorIsRefused(t *testing.T) {
	ix, _ := newAt(t)

	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), emptyVectorEmbedder{}); err == nil {
		t.Fatal("Sync accepted an empty vector")
	}
}

func TestSyncStopsWhenTheContextIsCancelled(t *testing.T) {
	ix, _ := newAt(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := ix.Sync(ctx, "bge-m3", docsOf("read"), &fakeEmbedder{}); err == nil {
		t.Fatal("Sync ran on a cancelled context")
	}
	if got := ix.Status().Indexed; got != 0 {
		t.Errorf("indexed = %d, want nothing", got)
	}
}

// A file that cannot be trusted is an error, not a partial load: half an
// index would pair one tool's text with another tool's vector.
func TestACorruptIndexIsAnError(t *testing.T) {
	t.Run("not our layout", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "embeddings.bin")
		if err := os.WriteFile(path, []byte("this is not an index"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := embedding.OpenIndex(path); err == nil {
			t.Error("OpenIndex accepted a file that is not an index")
		}
	})

	t.Run("a header describing vectors that are not there", func(t *testing.T) {
		ix, path := newAt(t)
		if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), &fakeEmbedder{}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw[:len(raw)-1], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := embedding.OpenIndex(path); err == nil {
			t.Error("OpenIndex accepted a truncated index")
		}
	})
}

// The layout is part of the promise: a length, a readable header, then
// the vectors. This pins it against a change that would make the file
// undiagnosable without saying so.
func TestTheFileIsALengthAReadableHeaderAndTheVectors(t *testing.T) {
	ix, path := newAt(t)
	if _, err := ix.Sync(context.Background(), "bge-m3", docsOf("read", "write"), &fakeEmbedder{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	headerLen := int(binary.LittleEndian.Uint32(raw[:4]))
	var h struct {
		Model     string `json:"model"`
		Dimension int    `json:"dimension"`
		Entries   []struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw[4:4+headerLen], &h); err != nil {
		t.Fatalf("the header is not readable JSON: %v", err)
	}
	if h.Model != "bge-m3" || h.Dimension != 3 || len(h.Entries) != 2 {
		t.Errorf("header = %+v, want the model, the width and both tools", h)
	}
	if got, want := len(raw[4+headerLen:]), len(h.Entries)*h.Dimension*4; got != want {
		t.Errorf("payload = %d bytes, want %d", got, want)
	}
}

// Reading the status while a round is inside a slow service call has to
// answer immediately, or the page showing progress would take as long as
// the build.
func TestStatusAnswersWhileAServiceCallIsInFlight(t *testing.T) {
	ix, _ := newAt(t)

	started := make(chan struct{})
	release := make(chan struct{})
	blocking := embedderFunc(func(_ context.Context, texts []string) ([][]float32, error) {
		close(started)
		<-release
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 2, 3}
		}
		return out, nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := ix.Sync(context.Background(), "bge-m3", docsOf("read"), blocking)
		done <- err
	}()

	<-started
	if status := ix.Status(); !status.Building {
		t.Error("building = false while a round is running")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if status := ix.Status(); status.Building {
		t.Error("building = true after the round finished")
	}
}
