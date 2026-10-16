package pipeline

// Mode is a translation mode as a recipe over the Steps: what to do per chapter,
// and whether an NMT draft already in the TM is a usable hit for it. Cost
// projection and pricing are not here — they are pure arithmetic over the cost
// model, so they live in the Stages (ProjectCost, PriceRun), which is the side
// that holds the model. Adding a mode is a new recipe here plus, if it needs a
// new step, one Steps method — never a new flow.
type Mode interface {
	// ReusesDrafts reports whether a raw NMT draft already in the TM is a usable
	// hit for this mode. The two modes disagree on exactly this.
	ReusesDrafts() bool
	// Chapter is the per-chapter recipe, composed of Steps calls through the flow.
	Chapter(f *flow, ch ChapterInfo) error
}

// selectMode picks the mode for in.
func selectMode(in BookInput) (Mode, error) {
	if runMode(in) == ModeLiterary {
		return literaryMode{}, nil
	}
	return nmtMode{}, nil
}

// nmtMode drafts every fresh segment with the NMT provider and optionally
// escalates the weakest to an LLM post-editor. An unescalated draft is the final
// output for its segment, which is why the mode reuses and persists drafts.
type nmtMode struct{}

func (nmtMode) ReusesDrafts() bool { return true }

// Chapter: Resolve → Draft → Escalate → Commit. Drafts are persisted (Draft
// with persist=true); nothing extra is persisted at commit.
func (nmtMode) Chapter(f *flow, ch ChapterInfo) error {
	plan, err := f.resolve(ch, true)
	if err != nil {
		return err
	}
	if len(plan.Fresh) > 0 {
		if err := f.draft(ch, plan.Fresh, true); err != nil {
			return err
		}
		if err := f.escalate(ch, plan.Fresh); err != nil {
			return err
		}
		f.priceReport(stageTranslate)
	}
	return f.commit(ch, false)
}

// literaryMode drafts a chapter's TM misses, rewrites every fresh segment with
// the LLM against the series bible, then persists only the finished
// translations. A raw draft is never served or persisted: serving one would
// skip refinement for that segment permanently.
type literaryMode struct{}

func (literaryMode) ReusesDrafts() bool { return false }

// Chapter: Resolve → Draft → RefineBatch × n → Commit (→ UpdateBible). Drafts
// are not persisted (Draft with persist=false); the commit persists the refined
// translations only after Restore accepts them.
func (literaryMode) Chapter(f *flow, ch ChapterInfo) error {
	plan, err := f.resolve(ch, false)
	if err != nil {
		return err
	}
	if len(plan.Fresh) > 0 {
		if err := f.draft(ch, plan.Fresh, false); err != nil {
			return err
		}
		if err := f.refine(ch, plan.Fresh); err != nil {
			return err
		}
	}
	if err := f.commit(ch, true); err != nil {
		return err
	}
	if f.in.UpdateBible {
		if err := f.updateBible(ch); err != nil {
			return err
		}
	}
	f.prevChapterID = ch.ID
	return nil
}
