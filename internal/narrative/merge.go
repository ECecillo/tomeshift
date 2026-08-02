package narrative

// MergeStats reports what a merge could not keep, so a lossy merge is visible
// instead of silent. Dropping input used to be a no-op: merging volume 2 into a
// bible holding volume 1 discarded every chapter of volume 2 because their IDs
// collided, with no error, no warning and no count.
type MergeStats struct {
	// DroppedSummaries counts incoming entries skipped because base already
	// held an entry with the same (volume, chapter) identity.
	DroppedSummaries int
}

// Lossy reports whether the merge discarded any incoming content.
func (s MergeStats) Lossy() bool { return s.DroppedSummaries > 0 }

// MergeBibles returns base enriched with incoming, without losing base content,
// plus a report of the incoming entries it could not keep. It is pure and
// deterministic so incremental seeding (e.g. a later volume) can be merged into
// an existing series bible instead of overwriting it.
//
// Rules:
//   - StyleGuide: keep base's when non-empty (don't clobber a curated guide);
//     otherwise take incoming's.
//   - Characters: matched by Name. Existing characters keep their fields unless
//     incoming provides a non-empty replacement; SampleFR is unioned.
//     Unknown characters are appended.
//   - Address: matched by (From,To). A changed Form updates the rule and appends
//     incoming history; unknown pairs are appended.
//   - Summaries: appended, deduped by (VolumeID, ChapterID) — base wins on a
//     true conflict, and the count of losers is reported.
//
// Version is not set here; the store assigns the next version on Save.
func MergeBibles(base, incoming Bible) (Bible, MergeStats) {
	out := base
	if out.SeriesID == "" {
		out.SeriesID = incoming.SeriesID
	}
	if out.StyleGuide == "" {
		out.StyleGuide = incoming.StyleGuide
	}

	out.Characters = mergeCharacters(base.Characters, incoming.Characters)
	out.Address = mergeAddress(base.Address, incoming.Address)

	var stats MergeStats
	out.Summaries, stats.DroppedSummaries = mergeSummaries(base.Summaries, incoming.Summaries)
	return out, stats
}

func mergeCharacters(base, incoming []CharacterVoice) []CharacterVoice {
	out := append([]CharacterVoice(nil), base...)
	idx := make(map[string]int, len(out))
	for i, c := range out {
		idx[c.Name] = i
	}
	for _, in := range incoming {
		i, ok := idx[in.Name]
		if !ok {
			idx[in.Name] = len(out)
			out = append(out, in)
			continue
		}
		cur := out[i]
		if in.Register != "" {
			cur.Register = in.Register
		}
		if in.Notes != "" {
			cur.Notes = in.Notes
		}
		cur.SampleFR = unionStrings(cur.SampleFR, in.SampleFR)
		out[i] = cur
	}
	return out
}

func mergeAddress(base, incoming []AddressRule) []AddressRule {
	out := append([]AddressRule(nil), base...)
	idx := make(map[[2]string]int, len(out))
	for i, r := range out {
		idx[[2]string{r.From, r.To}] = i
	}
	for _, in := range incoming {
		key := [2]string{in.From, in.To}
		i, ok := idx[key]
		if !ok {
			idx[key] = len(out)
			out = append(out, in)
			continue
		}
		cur := out[i]
		if in.Form != "" && in.Form != cur.Form {
			cur.Form = in.Form
			cur.History = append(cur.History, in.History...)
		}
		out[i] = cur
	}
	return out
}

// mergeSummaries appends incoming entries base does not already hold, keyed by
// (VolumeID, ChapterID), and returns how many it had to drop.
func mergeSummaries(base, incoming []Summary) ([]Summary, int) {
	out := append([]Summary(nil), base...)
	seen := make(map[[2]string]bool, len(out))
	for _, s := range out {
		seen[s.Key()] = true
	}

	dropped := 0
	for _, in := range incoming {
		if seen[in.Key()] {
			dropped++
			continue
		}
		seen[in.Key()] = true
		out = append(out, in)
	}
	return out, dropped
}

func unionStrings(base, incoming []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		seen[s] = true
	}
	for _, s := range incoming {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
