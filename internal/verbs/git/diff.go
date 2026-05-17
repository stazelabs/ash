package git

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/runner"
)

const (
	DiffDefaultContext    = 3
	DiffMaxContext        = 50
	DiffDefaultLimitBytes = 256 * 1024
	DiffMaxLimitBytes     = 4 * 1024 * 1024
)

// DiffResult is the structured replacement for `git diff` text scraping.
type DiffResult struct {
	Files          []DiffFile `msgpack:"files,omitempty"`
	TotalAdditions int        `msgpack:"total_additions"`
	TotalDeletions int        `msgpack:"total_deletions"`
	Truncated      bool       `msgpack:"truncated,omitempty"`
	TruncInfo      *proto.TruncInfo `msgpack:"truncation_hint,omitempty"`
	StatOnly       bool       `msgpack:"stat_only,omitempty"`
}

// DiffFile captures one changed file from a diff. In full (non-stat) mode,
// Patch holds the raw unified diff text for this file including the
// "diff --git" header so callers can render it as-is or parse further.
// In stat-only mode, Patch is empty and only Additions/Deletions are set.
//
// ASH-150: when a file's patch exceeds the per-result byte budget the
// emitted Patch is a hunk-aware fragment of the original; the
// PatchTruncated / HunksTotal / HunksShown / ContextElided fields
// describe how the fragment was assembled so MCP harnesses can detect
// partial patches programmatically. Stats (Additions, Deletions) always
// reflect the FULL file regardless of patch truncation.
type DiffFile struct {
	Path      string `msgpack:"path"`
	OldPath   string `msgpack:"old_path,omitempty"` // set for renames and copies
	Status    string `msgpack:"status"`             // A/D/M/R/C; empty in stat-only mode
	Binary    bool   `msgpack:"binary,omitempty"`
	Additions int    `msgpack:"additions"`
	Deletions int    `msgpack:"deletions"`
	Patch     string `msgpack:"patch,omitempty"`

	// PatchTruncated is true when Patch contains fewer bytes than the
	// raw upstream patch — either because hunks were dropped (multi-hunk
	// overflow), context lines were elided (single huge hunk), or the
	// patch was omitted entirely (per-file budget exhausted).
	PatchTruncated bool `msgpack:"patch_truncated,omitempty"`
	// HunksTotal is the number of hunks the upstream patch contained.
	// Zero when there were none (binary, pure rename, etc.) or when the
	// patch was not truncated (the field omits-on-zero on the wire).
	HunksTotal int `msgpack:"hunks_total,omitempty"`
	// HunksShown is how many hunks Patch actually contains. Counts a
	// context-elided hunk as 1.
	HunksShown int `msgpack:"hunks_shown,omitempty"`
	// ContextElided is true when one or more hunks in Patch had their
	// context (" "-prefixed) lines collapsed into an inline sentinel to
	// fit the budget. Changed lines (+ / -) are always preserved.
	ContextElided bool `msgpack:"context_elided,omitempty"`
}

// runDiffShellout shells out to system git for the diff op.
// Selected by [git].backend = "shellout" in ash.toml.
func runDiffShellout(a *Args, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	if a.StatOnly {
		return runDiffStat(a, tr)
	}
	return runDiffFull(a, tr)
}

func buildDiffArgs(extra []string, a *Args) []string {
	args := []string{"-C", a.Path, "diff"}
	args = append(args, extra...)
	if a.Staged {
		args = append(args, "--cached")
	}
	if a.Range != "" {
		// Range is a single git rev argument (e.g. "HEAD~1..HEAD" or "HEAD~1").
		// Passed as-is; git validates it.
		args = append(args, a.Range)
	}
	if a.Pathspec != "" {
		args = append(args, "--", a.Pathspec)
	}
	return args
}

// runDiffStat runs `git diff --numstat` for token-cheap per-file counts.
func runDiffStat(a *Args, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	res, perr := runner.Run("git", buildDiffArgs([]string{"--numstat"}, a), runner.Opts{Tracer: tr})
	if perr != nil {
		return nil, perr
	}
	if res.ExitCode != 0 {
		return nil, gitRunError(a.Path, res.Stderr)
	}
	return parseDiffNumstat(res.Stdout)
}

// runDiffFull runs `git diff --unified=N --no-color` and parses the
// unified diff into per-file DiffFile records.
func runDiffFull(a *Args, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	extra := []string{"--no-color", fmt.Sprintf("--unified=%d", a.Context)}
	res, perr := runner.Run("git", buildDiffArgs(extra, a), runner.Opts{Tracer: tr})
	if perr != nil {
		return nil, perr
	}
	if res.ExitCode != 0 {
		return nil, gitRunError(a.Path, res.Stderr)
	}
	return parseDiffUnified(res.Stdout, a.LimitBytes)
}

// parseDiffNumstat parses `git diff --numstat` output.
// Each line is: "<add>\t<del>\t<path>" where add/del are "-" for binary files.
func parseDiffNumstat(out []byte) (*DiffResult, *proto.Error) {
	res := &DiffResult{StatOnly: true}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		f := DiffFile{Path: fields[2]}
		if fields[0] == "-" {
			f.Binary = true
		} else {
			if a, err := strconv.Atoi(fields[0]); err == nil {
				f.Additions = a
				res.TotalAdditions += a
			}
			if d, err := strconv.Atoi(fields[1]); err == nil {
				f.Deletions = d
				res.TotalDeletions += d
			}
		}
		res.Files = append(res.Files, f)
	}
	if err := scanner.Err(); err != nil {
		return nil, &proto.Error{Code: "parse", Msg: err.Error()}
	}
	return res, nil
}

// parseDiffUnified parses the output of `git diff --no-color --unified=N`.
// Files are grouped by the "diff --git a/X b/Y" header lines. The raw
// patch text per file (including the diff header) is preserved in
// DiffFile.Patch. A limitBytes cap is applied to the total patch bytes
// returned; files beyond the cap have Patch="" but their stats are still
// included. Additions and deletions are counted from the + / - lines
// within hunks.
func parseDiffUnified(out []byte, limitBytes int) (*DiffResult, *proto.Error) {
	res := &DiffResult{}
	if len(out) == 0 {
		return res, nil
	}

	var (
		files      []DiffFile
		current    *DiffFile
		patchBuf   strings.Builder
		inHunk     bool
	)

	finalize := func() {
		if current == nil {
			return
		}
		current.Patch = patchBuf.String()
		res.TotalAdditions += current.Additions
		res.TotalDeletions += current.Deletions
		files = append(files, *current)
		current = nil
		patchBuf.Reset()
		inHunk = false
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "diff --git ") {
			finalize()
			rest := strings.TrimPrefix(line, "diff --git ")
			// Extract the new path from "a/<old> b/<new>". For renames,
			// rename-to lines below will override the path.
			newPath := ""
			if idx := strings.LastIndex(rest, " b/"); idx >= 0 {
				newPath = rest[idx+3:]
			}
			current = &DiffFile{Path: newPath, Status: "M"}
			patchBuf.WriteString(line)
			patchBuf.WriteByte('\n')
			inHunk = false
			continue
		}

		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(line, "new file mode"):
			current.Status = "A"
		case strings.HasPrefix(line, "deleted file mode"):
			current.Status = "D"
		case strings.HasPrefix(line, "rename from "):
			current.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			current.Status = "R"
			current.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "copy from "):
			current.OldPath = strings.TrimPrefix(line, "copy from ")
		case strings.HasPrefix(line, "copy to "):
			current.Status = "C"
			current.Path = strings.TrimPrefix(line, "copy to ")
		case strings.HasPrefix(line, "Binary files "):
			current.Binary = true
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case inHunk && len(line) > 0:
			switch line[0] {
			case '+':
				if !strings.HasPrefix(line, "+++") {
					current.Additions++
				}
			case '-':
				if !strings.HasPrefix(line, "---") {
					current.Deletions++
				}
			}
		}

		patchBuf.WriteString(line)
		patchBuf.WriteByte('\n')
	}
	finalize()

	if err := scanner.Err(); err != nil {
		return nil, &proto.Error{Code: "parse", Msg: err.Error()}
	}

	// ASH-150: apply the byte cap with hunk-aware degradation. Walk
	// files in order against a shared budget; per file, prefer dropping
	// trailing hunks over slicing mid-line, and fall back to
	// context-elision when a single huge hunk would otherwise drop the
	// whole patch (e.g. unified-diff context expanding across a 80 KiB
	// one-line JSON file). The shape of the degradation is recorded on
	// each DiffFile so MCP harnesses can detect partial patches without
	// scraping prose.
	totalBytes := 0
	for i := range files {
		patch := files[i].Patch
		remaining := limitBytes - totalBytes
		if remaining <= 0 {
			files[i].Patch = ""
			files[i].PatchTruncated = true
			res.Truncated = true
			continue
		}
		if len(patch) <= remaining {
			totalBytes += len(patch)
			continue
		}
		// Patch overflows the remaining budget — degrade hunk-aware.
		header, hunks := splitPatchHunks(patch)
		files[i].HunksTotal = len(hunks)
		if len(hunks) == 0 {
			// No hunks (binary marker, rename-only with no body). The
			// header is the entire useful content; include it if it
			// fits, otherwise drop.
			if len(header) <= remaining {
				files[i].Patch = header
				totalBytes += len(header)
			} else {
				files[i].Patch = ""
			}
			files[i].PatchTruncated = true
			res.Truncated = true
			continue
		}
		var out strings.Builder
		out.WriteString(header)
		used := len(header)
		shown := 0
		for _, h := range hunks {
			if used+len(h) > remaining {
				break
			}
			out.WriteString(h)
			used += len(h)
			shown++
		}
		if shown == 0 {
			// First hunk alone overflows. Try context-eliding it: drop
			// the surrounding " "-prefixed lines and keep only the
			// changes. This is the recipe that unblocks one-line
			// JSON / lockfile diffs.
			elided, kept, dropped, ok := elideHunkContext(hunks[0])
			if ok && used+len(elided) <= remaining && kept > 0 {
				out.WriteString(elided)
				fmt.Fprintf(&out, "[context elided: %d lines; %d changes kept]\n", dropped, kept)
				used += len(elided)
				files[i].ContextElided = true
				shown = 1
			}
		}
		omitted := len(hunks) - shown
		if omitted > 0 {
			fmt.Fprintf(&out, "[hunks: %d shown, %d omitted (patch_bytes=%d, budget=%d)]\n",
				shown, omitted, len(patch), remaining)
		}
		files[i].Patch = out.String()
		files[i].HunksShown = shown
		files[i].PatchTruncated = true
		totalBytes += out.Len()
		res.Truncated = true
	}

	res.Files = files
	if res.Truncated {
		res.TruncInfo = &proto.TruncInfo{Trunc: 1, Limit: limitBytes, Max: DiffMaxLimitBytes}
	}
	return res, nil
}

// splitPatchHunks splits a per-file unified-diff patch text into its
// pre-hunk header (everything up to the first "@@ " line) and a slice
// of hunk strings (each beginning with "@@ "). A patch with no hunks
// returns (patch, nil) — this happens for binary markers and pure
// rename/copy bodies. Lines inside hunk bodies are prefixed with ' ',
// '+', or '-' so "@@" at line start uniquely identifies a hunk header
// and the split is unambiguous.
func splitPatchHunks(patch string) (header string, hunks []string) {
	lines := strings.SplitAfter(patch, "\n")
	headerEnd := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "@@ ") {
			headerEnd = i
			break
		}
	}
	if headerEnd < 0 {
		return patch, nil
	}
	header = strings.Join(lines[:headerEnd], "")
	var cur strings.Builder
	for i := headerEnd; i < len(lines); i++ {
		ln := lines[i]
		if strings.HasPrefix(ln, "@@ ") && cur.Len() > 0 {
			hunks = append(hunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(ln)
	}
	if cur.Len() > 0 {
		hunks = append(hunks, cur.String())
	}
	return header, hunks
}

// elideHunkContext rebuilds a hunk with its " "-prefixed context lines
// collapsed into a single "[N context lines elided]" sentinel per run.
// Changed lines (+ / -) and the hunk header are preserved verbatim.
// Returns ok=false when the input is not a recognizable hunk (no "@@ "
// header line found in the first slot).
func elideHunkContext(hunk string) (out string, kept, elided int, ok bool) {
	lines := strings.SplitAfter(hunk, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "@@ ") {
		return "", 0, 0, false
	}
	var b strings.Builder
	b.WriteString(lines[0])
	run := 0
	flush := func() {
		if run > 0 {
			fmt.Fprintf(&b, " [%d context lines elided]\n", run)
			elided += run
			run = 0
		}
	}
	for i := 1; i < len(lines); i++ {
		ln := lines[i]
		if ln == "" {
			continue
		}
		switch ln[0] {
		case ' ':
			run++
		case '+', '-':
			flush()
			b.WriteString(ln)
			kept++
		default:
			// Trailing "\ No newline at end of file" or stray content.
			flush()
			b.WriteString(ln)
		}
	}
	flush()
	return b.String(), kept, elided, true
}

// diffTruncHint reconstructs the human-readable truncation message from
// structured TruncInfo. ASH-76.
func diffTruncHint(ti *proto.TruncInfo) string {
	if ti == nil {
		return ""
	}
	return fmt.Sprintf(
		"patch output exceeded %d bytes. --pathspec/--stat/--bytes.",
		ti.Limit,
	)
}

func prettyDiff(d *DiffResult) string {
	if d == nil {
		return "ok\n<empty diff>"
	}
	var b strings.Builder
	verb := "diff"
	if d.StatOnly {
		verb = "diff --stat"
	}
	fmt.Fprintf(&b, "§git %s: %d file(s) +%d -%d",
		verb, len(d.Files), d.TotalAdditions, d.TotalDeletions)
	if d.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteString("\n")

	if d.StatOnly {
		for _, f := range d.Files {
			if f.Binary {
				fmt.Fprintf(&b, "  binary          %s\n", f.Path)
			} else {
				fmt.Fprintf(&b, "  +%-5d  -%-5d  %s\n", f.Additions, f.Deletions, f.Path)
			}
		}
	} else {
		for _, f := range d.Files {
			if f.Patch != "" {
				b.WriteString(f.Patch)
			} else if f.Binary {
				fmt.Fprintf(&b, "[%s %s: binary file]\n", f.Status, f.Path)
			} else {
				fmt.Fprintf(&b, "[%s %s: +%d -%d (patch omitted, byte cap reached)]\n",
					f.Status, f.Path, f.Additions, f.Deletions)
			}
		}
	}

	if d.Truncated && d.TruncInfo != nil {
		b.WriteString("\n[truncation: ")
		b.WriteString(diffTruncHint(d.TruncInfo))
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}
