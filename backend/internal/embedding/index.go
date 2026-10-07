package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The index lives in one file: a JSON header and a float32 payload.
//
//	uint32 header length, little endian | header JSON | vectors
//
// One file rather than two so that a crash leaves either the old index
// or the new one, never a header describing vectors that are not in the
// file. The header is plain readable JSON — model, width, and one entry
// per tool — so the file can be diagnosed with ordinary tools.
//
// The vectors are one flat slice rather than a slice per tool: at
// directory scale that is one allocation instead of thirty thousand,
// and the slice is then exactly the payload the file holds.
const indexVersion = 1

// batchSize bounds how many texts go into one request. Providers cap
// both the number of inputs and the request size; 32 keeps a request in
// the tens of kilobytes and a thirty-thousand-tool build at about a
// thousand requests, which is what makes progress reporting worth
// having.
const batchSize = 32

// Key names one tool.
type Key struct {
	Server string
	Tool   string
}

// Document is one tool as the index sees it: which tool it is, and the
// text its vector is made from.
type Document struct {
	Server string
	Tool   string
	Text   string
}

// Entry is the header line for one indexed tool.
type Entry struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Hash   string `json:"hash"`
}

// Embedder turns texts into vectors, one vector per text in order.
// [Client] implements it; tests use a stand-in rather than a service.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Index holds one vector per tool.
type Index struct {
	path string

	mu      sync.Mutex
	model   string
	width   int
	entries []Entry
	vectors []float32
	at      map[Key]int

	// building and lastError are what an operator reads while a round is
	// running or after it failed. They live here so that the status is
	// one lock away from the data it describes.
	building  bool
	lastError string
	lastDone  int64 // unix seconds of the last round that finished
}

// NewIndex returns an empty index that saves to path.
func NewIndex(path string) *Index {
	return &Index{path: path}
}

// OpenIndex reads the index at path.
//
// A missing file is an empty index: nothing has been built yet. A file
// that cannot be read or does not make sense is an error, and the caller
// starts over from empty — an index that is half-trusted would attach
// one tool's text to another tool's vector, which no later round can
// detect.
func OpenIndex(path string) (*Index, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewIndex(path), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the index %s: %w", path, err)
	}

	ix := NewIndex(path)
	if err := ix.decode(raw); err != nil {
		return nil, fmt.Errorf("read the index %s: %w", path, err)
	}
	return ix, nil
}

// header is the readable part of the index file.
type header struct {
	Version   int     `json:"version"`
	Model     string  `json:"model"`
	Dimension int     `json:"dimension"`
	Entries   []Entry `json:"entries"`
}

func (ix *Index) decode(raw []byte) error {
	if len(raw) < 4 {
		return fmt.Errorf("the file is %d bytes, too short to hold a header", len(raw))
	}
	headerLen := int(binary.LittleEndian.Uint32(raw[:4]))
	if headerLen <= 0 || 4+headerLen > len(raw) {
		return fmt.Errorf("the file claims a %d-byte header, which does not fit in %d bytes",
			headerLen, len(raw))
	}

	var h header
	if err := json.Unmarshal(raw[4:4+headerLen], &h); err != nil {
		return fmt.Errorf("the header is not readable JSON: %w", err)
	}
	if h.Version != indexVersion {
		return fmt.Errorf("the index is version %d, and this build reads version %d",
			h.Version, indexVersion)
	}
	if h.Dimension < 0 || (len(h.Entries) > 0 && h.Dimension == 0) {
		return fmt.Errorf("the header describes %d entries at width %d",
			len(h.Entries), h.Dimension)
	}

	payload := raw[4+headerLen:]
	if len(payload) != len(h.Entries)*h.Dimension*4 {
		return fmt.Errorf("the header describes %d vectors of width %d, and the file holds %d bytes of vectors",
			len(h.Entries), h.Dimension, len(payload))
	}

	ix.model = h.Model
	ix.width = h.Dimension
	ix.entries = h.Entries
	ix.vectors = make([]float32, len(payload)/4)
	for i := range ix.vectors {
		ix.vectors[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[i*4:]))
	}
	ix.at = make(map[Key]int, len(ix.entries))
	for i, entry := range ix.entries {
		ix.at[Key{Server: entry.Server, Tool: entry.Tool}] = i
	}
	return nil
}

// Result says what one round did.
type Result struct {
	// Indexed is how many tools have a vector after the round.
	Indexed int
	// Total is how many the round was asked for. Indexed below Total
	// means the difference still has to be embedded.
	Total int
	// Embedded is how many texts this round had to make vectors for;
	// the rest were reused.
	Embedded int
}

// Sync brings the index in line with docs.
//
// A vector is reused when the text it was made from and the model that
// made it are unchanged — the stored hash covers both. Everything else
// is embedded in batches. Work that was finished is saved even when a
// later batch fails, so a service that keeps timing out still moves
// forward, and a tool whose text failed to embed is simply not indexed
// yet.
//
// The lock is held while the old state is read and while the new state
// is committed, and not while the service is being called: reading the
// status during a long build has to answer immediately, or the page
// that shows progress would take as long as the build.
//
// Sync is not meant to run concurrently with itself.
func (ix *Index) Sync(ctx context.Context, model string, docs []Document, embedder Embedder) (Result, error) {
	ix.mu.Lock()
	ix.building = true
	width, entries, vectors := ix.width, ix.entries, ix.vectors
	at := make(map[Key]int, len(entries))
	for i, entry := range entries {
		at[Key{Server: entry.Server, Tool: entry.Tool}] = i
	}
	ix.mu.Unlock()

	defer func() {
		ix.mu.Lock()
		ix.building = false
		ix.mu.Unlock()
	}()

	planned := make([]Entry, len(docs))
	reuseAt := make([]int, len(docs))
	var pending []int
	var texts []string

	for i, doc := range docs {
		hash := textHash(model, doc.Text)
		planned[i] = Entry{Server: doc.Server, Tool: doc.Tool, Hash: hash}
		reuseAt[i] = -1

		if j, ok := at[Key{Server: doc.Server, Tool: doc.Tool}]; ok &&
			width > 0 && entries[j].Hash == hash {
			reuseAt[i] = j
			continue
		}
		pending = append(pending, i)
		texts = append(texts, doc.Text)
	}

	// Vectors of reused tools point into the old slice, which nothing
	// mutates: the commit below replaces it wholesale.
	made := make([][]float32, len(docs))
	for i, j := range reuseAt {
		if j >= 0 {
			made[i] = vectors[j*width : (j+1)*width]
		}
	}

	embedded := 0
	var failure error

	for start := 0; start < len(texts); start += batchSize {
		if err := ctx.Err(); err != nil {
			failure = err
			break
		}
		end := min(start+batchSize, len(texts))

		answered, err := embedder.Embed(ctx, texts[start:end])
		if err != nil {
			failure = err
			break
		}
		if len(answered) != end-start {
			failure = fmt.Errorf("the service answered %d vectors for %d texts", len(answered), end-start)
			break
		}
		if len(answered[0]) == 0 {
			failure = errors.New("the service answered with an empty vector")
			break
		}

		// A width that differs from the index's means the service is not
		// answering with the model the index was built by, whatever the
		// name says. Nothing already indexed can be trusted alongside the
		// new vectors, so the index is cleared and the next round starts
		// over.
		if width != 0 && len(answered[0]) != width {
			ix.mu.Lock()
			ix.clearLocked()
			ix.mu.Unlock()
			if raw, err := encodeIndex("", 0, nil, nil); err == nil {
				_ = writeFileAtomic(ix.path, raw)
			}
			return Result{Total: len(docs)}, fmt.Errorf(
				"the service answered with vectors of width %d, and the index was built at width %d; "+
					"the index has been cleared and will be rebuilt", len(answered[0]), width)
		}
		width = len(answered[0])

		for i, vector := range answered {
			made[pending[start+i]] = vector
			embedded++
		}
	}

	// Only tools that have a vector are kept. A pending tool whose batch
	// failed is not indexed at all, which is the truth, and the count of
	// what is missing is what the status reports.
	savedEntries := make([]Entry, 0, len(planned))
	saved := make([]float32, 0, len(planned)*width)
	for i := range planned {
		if made[i] == nil {
			continue
		}
		savedEntries = append(savedEntries, planned[i])
		saved = append(saved, made[i]...)
	}

	ix.mu.Lock()
	ix.model = model
	ix.width = width
	ix.entries = savedEntries
	ix.vectors = saved
	ix.at = make(map[Key]int, len(savedEntries))
	for i, entry := range savedEntries {
		ix.at[Key{Server: entry.Server, Tool: entry.Tool}] = i
	}
	if failure == nil {
		ix.lastError = ""
		ix.lastDone = time.Now().Unix()
	} else {
		ix.lastError = failure.Error()
	}
	ix.mu.Unlock()

	if raw, err := encodeIndex(model, width, savedEntries, saved); err != nil {
		if failure == nil {
			failure = err
		}
	} else if err := writeFileAtomic(ix.path, raw); err != nil && failure == nil {
		failure = err
	}
	if failure != nil {
		ix.mu.Lock()
		ix.lastError = failure.Error()
		ix.mu.Unlock()
	}

	return Result{Indexed: len(savedEntries), Total: len(docs), Embedded: embedded}, failure
}

// clearLocked forgets everything, keeping the file path.
func (ix *Index) clearLocked() {
	ix.model, ix.width = "", 0
	ix.entries, ix.vectors, ix.at = nil, nil, make(map[Key]int)
}

// encodeIndex lays out the file described at the top of this file.
func encodeIndex(model string, width int, entries []Entry, vectors []float32) ([]byte, error) {
	h := header{Version: indexVersion, Model: model, Dimension: width, Entries: entries}
	encoded, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("encode the index header: %w", err)
	}

	raw := make([]byte, 4+len(encoded)+len(vectors)*4)
	binary.LittleEndian.PutUint32(raw[:4], uint32(len(encoded)))
	copy(raw[4:], encoded)
	for i, value := range vectors {
		binary.LittleEndian.PutUint32(raw[4+len(encoded)+i*4:], math.Float32bits(value))
	}
	return raw, nil
}

// Status is what an operator can learn about the index.
type Status struct {
	Model     string `json:"model,omitempty"`
	Dimension int    `json:"dimension,omitempty"`
	Indexed   int    `json:"indexed"`

	// Building is true while a round is running, which is when the
	// counts are moving.
	Building bool `json:"building"`

	// LastError is the last round's failure, empty after a round that
	// finished.
	LastError string `json:"lastError,omitempty"`

	// LastDone is when a round last finished, in unix seconds; zero when
	// none has.
	LastDone int64 `json:"lastDone,omitempty"`
}

// Status reads the current state.
func (ix *Index) Status() Status {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	return Status{
		Model:     ix.model,
		Dimension: ix.width,
		Indexed:   len(ix.entries),
		Building:  ix.building,
		LastError: ix.lastError,
		LastDone:  ix.lastDone,
	}
}

// Vector returns the vector for one tool, if it has one. The slice
// belongs to the index and must not be modified.
func (ix *Index) Vector(k Key) ([]float32, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	at, ok := ix.at[k]
	if !ok || ix.width == 0 {
		return nil, false
	}
	return ix.vectors[at*ix.width : (at+1)*ix.width], true
}

// Width is the vector width, zero when nothing is indexed.
func (ix *Index) Width() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.width
}

// textHash covers the text and the model together: a vector made by one
// model is not an answer to the same text asked of another. A model
// swapped on the service side under the same name changes nothing here,
// which is why the width is checked separately during a round.
func textHash(model, text string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// writeFileAtomic writes data by way of a temporary file in the same
// directory, renamed into place, so that a crash mid-write leaves the
// old file rather than a truncated one.
//
// config.Save and usage.Flush each carry their own copy of this. Three
// copies across three packages without a shared place is not yet worth
// a package of its own; a fourth would be, and this comment is where
// that shows up.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
