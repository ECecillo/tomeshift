package pipeline

// SeriesHooks lets the caller wire per-book concerns the flow itself does not
// own — the per-book logger and progress bar (Prepare), the reference-based
// score (Score), and any teardown or printing (Done) — around the sequential
// run RunSeries drives. All are optional.
type SeriesHooks struct {
	// Prepare runs before each book, on the shared Stages/Runtime: swap the
	// per-book logger and context, reset the bar.
	Prepare func(in BookInput)
	// Score computes a reference-based score for a produced book; nil disables.
	Score func(bookID, outputEPUB string) (float64, bool)
	// Done runs after each book, with its outcome and the raw run error (nil on
	// success), so the caller can map the failure to an exit code and print.
	Done func(in BookInput, outcome BookOutcome, err error)
}

// RunSeries translates a series of volumes in order through one shared engine,
// mirroring the batch command's semantics: it continues on a per-book failure
// and aggregates every outcome into a BatchSummary. Sequential is not an
// accident — the shared TM's single writer and (for literary) the bible's
// chapter order both forbid running volumes in parallel.
func RunSeries(steps Steps, rt Runtime, books []BookInput, hooks SeriesHooks) BatchSummary {
	outcomes := make([]BookOutcome, 0, len(books))
	for _, book := range books {
		if rt.Cancelled() {
			break
		}
		if hooks.Prepare != nil {
			hooks.Prepare(book)
		}

		res, err := rt.RunBook(book)
		result := res
		outcome := BookOutcome{BookID: book.BookID, RunID: book.RunID, OK: err == nil, Result: &result}
		if err != nil {
			outcome.Error = err.Error()
		} else if hooks.Score != nil {
			if score, ok := hooks.Score(book.BookID, res.OutputEPUB); ok {
				outcome.ChrF = &score
			}
		}

		if hooks.Done != nil {
			hooks.Done(book, outcome, err)
		}
		outcomes = append(outcomes, outcome)
	}
	return NewBatchSummary(outcomes)
}
