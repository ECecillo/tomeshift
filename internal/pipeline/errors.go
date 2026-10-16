package pipeline

import (
	"errors"
	"fmt"
)

// Stage identifies a pipeline stage for error attribution and exit-code mapping.
type Stage string

const (
	StageExtract   Stage = "extract"
	StageParse     Stage = "parse"
	StageSegment   Stage = "segment"
	StageTranslate Stage = "translate"
	StageRecompose Stage = "recompose"
	StageQA        Stage = "qa"
)

// ErrQAGateFailed is returned (wrapped) when QA thresholds or epubcheck fail.
var ErrQAGateFailed = errors.New("pipeline: QA gate failed")

// ErrChapterUnrefined is returned (wrapped) when a literary chapter's every
// batch degraded and no segment was refined: the chapter would ship NMT drafts
// literary mode does not persist, so the run fails instead of reading as done.
var ErrChapterUnrefined = errors.New("pipeline: literary chapter refined no segment")

// StageError attributes a failure to a specific pipeline stage so callers can
// map it to the right process exit code.
type StageError struct {
	Stage Stage
	Err   error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("pipeline stage %s failed: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }
