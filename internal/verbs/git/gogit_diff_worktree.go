package git

// ASH-66 worktree patch text. The range-diff path (gogit_diff.go)
// reuses go-git's object.Tree.Patch + UnifiedEncoder to render full
// unified-diff text. Working-tree diffs cannot use that helper because
// go-git does not expose a "diff between worktree/index and HEAD" patch
// generator: we have to build our own format/diff.FilePatch and feed
// it to the same encoder.
//
// The implementation here is the symmetric companion to
// diffGogitStaged / diffGogitUnstaged in gogit_diff.go. Those functions
// gather (before, after) string pairs for every changed file; the
// helpers in this file convert those pairs into format/diff types so
// the existing UnifiedEncoder produces a canonical "diff --git a/X b/Y"
// + hunk render, which then flows through parseDiffUnified for cap +
// per-file structure (same code path as range diffs).

import (
	"bytes"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// customFile implements diff.File for a single side of a working-tree
// diff. The hash is synthetic for worktree content (computed via
// plumbing.ComputeHash so two distinct contents always have distinct
// hashes — required by UnifiedEncoder, which skips body emission when
// from.Hash() == to.Hash()).
type customFile struct {
	hash plumbing.Hash
	mode filemode.FileMode
	path string
}

func (f *customFile) Hash() plumbing.Hash      { return f.hash }
func (f *customFile) Mode() filemode.FileMode  { return f.mode }
func (f *customFile) Path() string             { return f.path }

// customChunk implements diff.Chunk: one Equal / Add / Delete span of
// the unified-diff transform.
type customChunk struct {
	content string
	op      diff.Operation
}

func (c *customChunk) Content() string        { return c.content }
func (c *customChunk) Type() diff.Operation   { return c.op }

// customFilePatch implements diff.FilePatch for a single (before, after)
// pair. New files set from=nil; deleted files set to=nil; modified
// files set both.
type customFilePatch struct {
	from, to *customFile
	chunks   []diff.Chunk
	isBinary bool
}

func (p *customFilePatch) IsBinary() bool { return p.isBinary }
func (p *customFilePatch) Chunks() []diff.Chunk { return p.chunks }
func (p *customFilePatch) Files() (diff.File, diff.File) {
	// Returning typed-nil through the interface is correct here: the
	// encoder switches on nil to emit "new file mode" / "deleted file
	// mode" headers.
	var from, to diff.File
	if p.from != nil {
		from = p.from
	}
	if p.to != nil {
		to = p.to
	}
	return from, to
}

// customPatch wraps a slice of customFilePatch for the encoder's
// Patch.FilePatches() contract.
type customPatch struct {
	files []diff.FilePatch
}

func (p *customPatch) FilePatches() []diff.FilePatch { return p.files }
func (p *customPatch) Message() string               { return "" }

// buildLineChunks runs a line-level diff between before and after,
// returning []diff.Chunk suitable for a customFilePatch. Uses
// sergi/go-diff's line preprocessor (DiffLinesToChars) for speed on
// large files: the diff runs over a tiny one-char-per-line encoding
// and we expand back to real lines via DiffCharsToLines.
func buildLineChunks(before, after string) []diff.Chunk {
	if before == after {
		return nil
	}
	dmp := diffmatchpatch.New()
	a, b, lines := dmp.DiffLinesToChars(before, after)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, lines)
	out := make([]diff.Chunk, 0, len(diffs))
	for _, d := range diffs {
		var op diff.Operation
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			op = diff.Equal
		case diffmatchpatch.DiffInsert:
			op = diff.Add
		case diffmatchpatch.DiffDelete:
			op = diff.Delete
		}
		out = append(out, &customChunk{content: d.Text, op: op})
	}
	return out
}

// encodeCustomPatches renders a slice of FilePatches as unified-diff
// text with the given context line count. The output shape is
// byte-identical to what `git diff` emits for the same inputs (modulo
// rename-detection thresholds, which go-git's UnifiedEncoder does not
// implement — ASH-66 out-of-scope).
func encodeCustomPatches(fps []diff.FilePatch, ctx int) (string, error) {
	if len(fps) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	enc := diff.NewUnifiedEncoder(&buf, ctx)
	if err := enc.Encode(&customPatch{files: fps}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// makeFile builds a customFile with a content-derived hash. Path and
// mode are passed in by the caller (mode is filemode.Regular for
// ordinary files; symlink / submodule / executable handling is
// out-of-scope for this pass).
func makeFile(path, content string) *customFile {
	return &customFile{
		hash: plumbing.ComputeHash(plumbing.BlobObject, []byte(content)),
		mode: filemode.Regular,
		path: path,
	}
}

// modFilePatch builds a customFilePatch for a modified file: both
// sides populated, with synthetic but distinct hashes.
func modFilePatch(path, before, after string) *customFilePatch {
	return &customFilePatch{
		from:   makeFile(path, before),
		to:     makeFile(path, after),
		chunks: buildLineChunks(before, after),
	}
}

// addFilePatch builds a customFilePatch for a newly added file:
// from=nil, to populated with the file's content.
func addFilePatch(path, after string) *customFilePatch {
	return &customFilePatch{
		to:     makeFile(path, after),
		chunks: buildLineChunks("", after),
	}
}

// delFilePatch builds a customFilePatch for a deleted file: from
// populated, to=nil.
func delFilePatch(path, before string) *customFilePatch {
	return &customFilePatch{
		from:   makeFile(path, before),
		chunks: buildLineChunks(before, ""),
	}
}
