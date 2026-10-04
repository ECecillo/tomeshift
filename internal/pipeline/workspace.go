package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ECecillo/ebooks-translator/internal/segment"
)

// workDirName is the run-scoped directory holding one unit file per chapter.
const workDirName = "work"

// Workspace is the run's per-chapter content store. Segmentation is
// deterministic — ids by position, hash by normalised text — so a process that
// holds only the units can re-parse the source document, re-derive the
// placeholders and restore. The units are exactly the segments.jsonl schema
// (§16.1) minus the run/book ids, which the artifact re-attaches.
//
// It is how content crosses the ports without a live *html.Node: an activity
// reads the units it needs, mutates their target text or status, and writes
// them back. One file per chapter keeps a chapter the unit of work.
type Workspace struct {
	Dir string
}

// NewWorkspace returns the workspace rooted under outputDir/work.
func NewWorkspace(outputDir string) Workspace {
	return Workspace{Dir: filepath.Join(outputDir, workDirName)}
}

// chapterPath is the unit file for chapterID.
func (w Workspace) chapterPath(chapterID string) string {
	return filepath.Join(w.Dir, chapterID+".jsonl")
}

// WriteChapter writes units as newline-delimited JSON, atomically: a temp file
// in the same directory is renamed over the target, so a reader never sees a
// half-written chapter and a crash never corrupts a committed one.
func (w Workspace) WriteChapter(chapterID string, units []segment.Unit) error {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return fmt.Errorf("unable to create workspace dir %q, err: %w", w.Dir, err)
	}

	tmp, err := os.CreateTemp(w.Dir, chapterID+".*.tmp")
	if err != nil {
		return fmt.Errorf("unable to create workspace temp for chapter %s, err: %w", chapterID, err)
	}
	tmpName := tmp.Name()
	// On any early return the temp file is orphaned; remove it. After a
	// successful rename it no longer exists, so the remove is a harmless no-op.
	defer func() { _ = os.Remove(tmpName) }()

	bw := bufio.NewWriter(tmp)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false) // units carry placeholder tokens like <x id="1"/>
	for i := range units {
		if err := enc.Encode(units[i]); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("unable to encode unit %s, err: %w", units[i].ID, err)
		}
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("unable to flush workspace chapter %s, err: %w", chapterID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("unable to close workspace temp for chapter %s, err: %w", chapterID, err)
	}
	if err := os.Rename(tmpName, w.chapterPath(chapterID)); err != nil {
		return fmt.Errorf("unable to commit workspace chapter %s, err: %w", chapterID, err)
	}
	return nil
}

// ReadChapter reads a chapter's units back in written order. A line that does
// not decode aborts the read: a corrupt unit file cannot be silently
// half-applied.
func (w Workspace) ReadChapter(chapterID string) ([]segment.Unit, error) {
	f, err := os.Open(w.chapterPath(chapterID))
	if err != nil {
		return nil, fmt.Errorf("unable to open workspace chapter %s, err: %w", chapterID, err)
	}
	defer func() { _ = f.Close() }()

	var units []segment.Unit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var u segment.Unit
		if err := json.Unmarshal(line, &u); err != nil {
			return nil, fmt.Errorf("unable to decode workspace chapter %s, err: %w", chapterID, err)
		}
		units = append(units, u)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("unable to read workspace chapter %s, err: %w", chapterID, err)
	}
	return units, nil
}
