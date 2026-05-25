package initverb

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stazelabs/ash/internal/atomicwrite"
	"github.com/stazelabs/ash/internal/proto"
)

// Section markers that bracket the ash-managed block in a target repo's
// guidance file. Public so uninit can use them.
const (
	GuidanceBeginMarker = "<!-- ash:begin -->"
	GuidanceEndMarker   = "<!-- ash:end -->"
)

// CandidateGuidanceFiles lists the agent-guidance file names ash will write
// to or merge into, in preference order. The first file that already exists
// wins; if none exist, the first name is created.
var CandidateGuidanceFiles = []string{"CLAUDE.md", "AGENTS.md"}

//go:embed template.md
var guidanceBody string

// guidanceSectionRE captures a full begin..end block including the trailing
// newline if present. Permissive on line endings so a CRLF-checked-in file
// is still recognized.
var guidanceSectionRE = regexp.MustCompile(
	`(?ms)^<!-- ash:begin -->\r?\n(.*?)\r?\n<!-- ash:end -->\r?\n?`,
)

// pickGuidancePath returns the path ash should write its guidance to.
// Preference: existing CLAUDE.md > existing AGENTS.md > new CLAUDE.md.
// The returned path is always inside root.
func pickGuidancePath(root string) string {
	for _, name := range CandidateGuidanceFiles {
		p := filepath.Join(root, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(root, CandidateGuidanceFiles[0])
}

// renderSection returns the begin..end block with the current template body.
func renderSection() string {
	body := strings.TrimSpace(guidanceBody)
	return GuidanceBeginMarker + "\n" + body + "\n" + GuidanceEndMarker + "\n"
}

// guidanceMode controls how updateGuidance reconciles the on-disk file
// with the embedded template (ASH-231).
type guidanceMode int

const (
	// guidanceInstall is the default: create the file if missing, append a
	// section if no markers exist, and warn (without writing) if a
	// marker-bracketed section's content has drifted from the current
	// template — surfaces refreshAvailable=true to the caller.
	guidanceInstall guidanceMode = iota
	// guidanceForce overwrites a drifted marker-bracketed section in place
	// (and otherwise behaves like guidanceInstall).
	guidanceForce
	// guidanceRefresh is the narrow update mode: only rewrites an existing
	// marker-bracketed section whose content has drifted. Never creates the
	// file, never appends to a file with no markers — those produce
	// warnings instead, since the caller asked for a "refresh" and an
	// install-style action would surprise them.
	guidanceRefresh
)

// updateGuidance writes (or merges) the agent-guidance template into the
// target repo's guidance file. Returns:
//
//	changed:          true if the file was written
//	alreadyInstalled: true if the canonical section was already present (no-op)
//	refreshAvailable: true in guidanceInstall mode when the section exists
//	                  but its content differs from the current template — a
//	                  `ash init --refresh` would update it
//	path:             the file we touched (or would have touched)
//	warning:          non-empty if a different ash section exists and mode
//	                  was guidanceInstall, or refresh was requested but no
//	                  marker-bracketed section was found
//	perr:             non-nil on IO/parse errors
func updateGuidance(root string, mode guidanceMode) (changed, alreadyInstalled, refreshAvailable bool, path, warning string, perr *proto.Error) {
	path = pickGuidancePath(root)
	section := renderSection()

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, false, false, path, "", &proto.Error{Code: "guidance_read", Msg: err.Error()}
		}
		// File does not exist.
		if mode == guidanceRefresh {
			warning = fmt.Sprintf("guidance file not found at %s; run `ash init` first", path)
			return false, false, false, path, warning, nil
		}
		if werr := atomicwrite.Write(path, []byte(section), atomicwrite.Options{TmpPrefix: ".ash-init-"}); werr != nil {
			return false, false, false, path, "", &proto.Error{Code: "guidance_write", Msg: werr.Error()}
		}
		return true, false, false, path, "", nil
	}

	body := string(data)
	if loc := guidanceSectionRE.FindStringIndex(body); loc != nil {
		existing := body[loc[0]:loc[1]]
		if existing == section {
			return false, true, false, path, "", nil
		}
		// Section exists but differs from current template.
		if mode == guidanceInstall {
			warning = fmt.Sprintf("existing ash guidance section in %s differs from current template; run `ash init --refresh` to update, or `ash init --force` to override", path)
			return false, false, true, path, warning, nil
		}
		// guidanceRefresh or guidanceForce: rewrite the section in place.
		newBody := body[:loc[0]] + section + body[loc[1]:]
		if werr := atomicwrite.Write(path, []byte(newBody), atomicwrite.Options{TmpPrefix: ".ash-init-"}); werr != nil {
			return false, false, false, path, "", &proto.Error{Code: "guidance_write", Msg: werr.Error()}
		}
		return true, false, false, path, "", nil
	}

	// File exists but has no ash markers.
	if mode == guidanceRefresh {
		warning = fmt.Sprintf("no ash markers found in %s; run `ash init` to install", path)
		return false, false, false, path, warning, nil
	}
	// Install or Force: append a fresh section after the existing content.
	// Normalize the seam (blank line of separation, predictable trailing
	// newline) so the regex round-trips cleanly on the next call.
	prefix := body
	switch {
	case prefix == "":
		// No existing content: just write the section.
	case strings.HasSuffix(prefix, "\n\n"):
		// Already a blank line: leave as-is.
	case strings.HasSuffix(prefix, "\n"):
		prefix += "\n"
	default:
		prefix += "\n\n"
	}
	out := prefix + section
	if werr := atomicwrite.Write(path, []byte(out), atomicwrite.Options{TmpPrefix: ".ash-init-"}); werr != nil {
		return false, false, false, path, "", &proto.Error{Code: "guidance_write", Msg: werr.Error()}
	}
	return true, false, false, path, "", nil
}

// StripGuidance removes the ash-managed section from the first candidate
// guidance file in root that contains it. Returns (changed, path, perr).
// A file with no ash markers is left untouched. Exported so uninit can call
// it without a circular dependency.
func StripGuidance(root string) (bool, string, *proto.Error) {
	for _, name := range CandidateGuidanceFiles {
		p := filepath.Join(root, name)
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, p, &proto.Error{Code: "guidance_read", Msg: err.Error()}
		}
		loc := guidanceSectionRE.FindStringIndex(string(data))
		if loc == nil {
			continue
		}
		body := string(data)
		newBody := body[:loc[0]] + body[loc[1]:]
		// Trim trailing whitespace introduced by removing a section that
		// sat at the end of the file. Keep one trailing newline if any
		// content remains.
		newBody = strings.TrimRight(newBody, "\n\t ")
		if newBody != "" {
			newBody += "\n"
		}
		if werr := atomicwrite.Write(p, []byte(newBody), atomicwrite.Options{TmpPrefix: ".ash-uninit-"}); werr != nil {
			return false, p, &proto.Error{Code: "guidance_write", Msg: werr.Error()}
		}
		return true, p, nil
	}
	return false, "", nil
}
