# Implementation Plan: EPUB Parse/Segment/Recompose Pipeline

- Status: `planned`
- Created: 2026-03-08
- Related RFC: `docs/rfcs/0004-epub-pipeline.md`
- Milestone: M1 Core MVP

## Development Methodology

All implementation follows **Test-Driven Development (TDD)**:

1. **RED**: Write failing tests that define expected behavior
2. **GREEN**: Write minimal code to make tests pass
3. **REFACTOR**: Clean up while keeping tests green

This ensures high test coverage, better design through testability, and confidence in the implementation.

---

## Development Phase 1: Project Setup & Dependencies

### Description

Set up project foundation with dependencies and directory structure.

### What We Will Do

1. Add `golang.org/x/net/html` dependency for robust XHTML parsing
2. Create directory structure for `internal/epub`, `internal/segment`, `internal/pipeline`
3. Add `doc.go` files for each package

### Files to Create

- `internal/epub/doc.go`
- `internal/segment/doc.go`
- `internal/pipeline/doc.go`

### Files to Modify

- `go.mod` - Add dependency

### Commands

```bash
go get golang.org/x/net/html
go build ./...
```

### Acceptance Criteria

- [ ] `go get golang.org/x/net/html` completes without errors
- [ ] `go build ./...` passes
- [ ] `go mod tidy` shows no changes
- [ ] All `doc.go` files exist with proper package documentation
- [ ] `golangci-lint run` passes

---

## Development Phase 2: EPUB Extraction & Parsing (TDD)

### Description

Implement EPUB extraction and parsing using TDD. This phase handles reading EPUB archives, validating their structure, parsing the OPF manifest/spine, and building an in-memory representation of the book.

### TDD Workflow

| Step | Phase | Action |
|------|-------|--------|
| 2.1 | RED | Write `extractor_test.go` with table-driven tests for valid/invalid EPUB extraction |
| 2.2 | GREEN | Implement `errors.go`, `types.go`, `extractor.go` |
| 2.3 | RED | Write `parser_test.go` for OPF parsing, manifest, spine order |
| 2.4 | GREEN | Add types, implement `parser.go` |
| 2.5 | RED | Write `xhtml_test.go` for paragraph detection |
| 2.6 | GREEN | Implement `xhtml.go` |
| 2.7 | REFACTOR | Review and clean up |

### Files to Create (in order)

1. `internal/epub/extractor_test.go` - Tests first
2. `internal/epub/errors.go` - Sentinel errors
3. `internal/epub/types.go` - RawEPUB, Book, Manifest, SpineItem, XHTMLDocument
4. `internal/epub/extractor.go` - Extractor interface + ZipExtractor
5. `internal/epub/parser_test.go` - Tests first
6. `internal/epub/parser.go` - Parser interface + OPFParser
7. `internal/epub/xhtml_test.go` - Tests first
8. `internal/epub/xhtml.go` - XHTML parsing helpers

### Test Cases

**Extraction Tests:**
- Valid EPUB extraction succeeds
- Missing `mimetype` file returns `ErrMissingMimetype`
- Missing `META-INF/container.xml` returns `ErrMissingContainer`
- Invalid ZIP file returns appropriate error

**Parser Tests:**
- Valid OPF parsing extracts manifest items
- Valid OPF parsing extracts spine order
- Invalid `container.xml` returns `ErrMissingOPF`
- Manifest item properties are correctly parsed

**XHTML Tests:**
- Detects `<p>` elements as paragraph nodes
- Detects `<div>` elements as paragraph nodes
- Detects `<h1>`-`<h6>` elements as heading nodes
- Handles nested structures correctly

### Acceptance Criteria

- [ ] Tests written BEFORE implementation for each component
- [ ] `ZipExtractor.Extract()` extracts valid EPUBs
- [ ] `ZipExtractor.Extract()` returns `ErrMissingMimetype` for missing mimetype
- [ ] `ZipExtractor.Extract()` returns `ErrMissingContainer` for missing container.xml
- [ ] `OPFParser.Parse()` extracts manifest items and spine order
- [ ] XHTML parsing identifies paragraph-like nodes (`<p>`, `<div>`, `<h1>`-`<h6>`)
- [ ] All tests pass: `env -u GOROOT go test ./internal/epub/...`
- [ ] Test coverage >= 80%
- [ ] `gofmt -w -s .` shows no changes
- [ ] `golangci-lint run` passes

---

## Development Phase 3: Text Segmentation & Placeholder Management (TDD)

### Description

Implement text segmentation layer using TDD. This phase handles paragraph-level segmentation, text normalization, hash computation, and inline markup placeholder tokenization/restoration.

### TDD Workflow

| Step | Phase | Action |
|------|-------|--------|
| 3.1 | RED | Write `normalizer_test.go` for whitespace/quote normalization |
| 3.2 | GREEN | Implement `normalizer.go` |
| 3.3 | RED | Write `hasher_test.go` with known SHA256 outputs |
| 3.4 | GREEN | Implement `hasher.go` |
| 3.5 | RED | Write `placeholder_test.go` including fuzz tests |
| 3.6 | GREEN | Implement `errors.go`, `types.go`, `placeholder.go` |
| 3.7 | RED | Write `segmenter_test.go` for paragraph extraction |
| 3.8 | GREEN | Implement `segmenter.go` |
| 3.9 | REFACTOR | Review and clean up |

### Files to Create (in order)

1. `internal/segment/normalizer_test.go` - Tests first
2. `internal/segment/normalizer.go`
3. `internal/segment/hasher_test.go` - Tests first
4. `internal/segment/hasher.go`
5. `internal/segment/placeholder_test.go` - Tests first (with fuzz)
6. `internal/segment/errors.go` - ErrPlaceholderMismatch
7. `internal/segment/types.go` - Segment, Placeholder, PlaceholderType, SegmentStatus
8. `internal/segment/placeholder.go` - PlaceholderManager
9. `internal/segment/segmenter_test.go` - Tests first
10. `internal/segment/segmenter.go` - Segmenter interface + ParagraphSegmenter

### Test Cases

**Normalizer Tests:**
- Multiple spaces collapse to single space
- Different quote styles normalize consistently
- Leading/trailing whitespace is trimmed
- Newlines within text are handled consistently

**Hasher Tests:**
- Known input produces expected SHA256 output
- Empty string produces correct hash
- Unicode text hashes correctly

**Placeholder Tests:**
- `<em>` tags tokenize to `<x id="N"/>` format
- `<strong>` tags tokenize correctly
- `<a href="...">` links tokenize and restore with attributes
- Ruby tags `<ruby>...<rt>...</rt></ruby>` tokenize correctly
- Nested tags tokenize in correct order
- Round-trip: `Restore(Tokenize(text))` equals original

**Segmenter Tests:**
- Single paragraph extracts one segment
- Multiple paragraphs extract multiple segments
- Segment IDs are unique and stable
- Node paths follow XPath-like format

### Acceptance Criteria

- [ ] Tests written BEFORE implementation for each component
- [ ] `Normalizer.Normalize()` produces consistent output
- [ ] `Hasher.Hash()` produces SHA256 of normalized text
- [ ] `PlaceholderManager.Tokenize()` replaces inline markup with `<x id="N"/>` tokens
- [ ] `PlaceholderManager.Restore()` correctly reinserts original markup
- [ ] Placeholder round-trip equals original for all test cases
- [ ] Fuzz test passes with 10,000+ iterations
- [ ] `ParagraphSegmenter.Segment()` extracts one segment per paragraph
- [ ] Segment IDs are unique and stable
- [ ] Node paths are correct (XPath-like)
- [ ] All tests pass: `env -u GOROOT go test ./internal/segment/...`
- [ ] Test coverage >= 80%
- [ ] `gofmt -w -s .` shows no changes
- [ ] `golangci-lint run` passes

---

## Development Phase 4: EPUB Recomposition (TDD)

### Description

Implement EPUB recomposition using TDD. This phase handles reinserting translated text into XHTML nodes and packaging the final EPUB with correct structure.

### TDD Workflow

| Step | Phase | Action |
|------|-------|--------|
| 4.1 | RED | Write `recomposer_test.go` for archive creation, structure validation |
| 4.2 | GREEN | Implement `recomposer.go` |
| 4.3 | REFACTOR | Review EPUB spec compliance |

### Files to Create (in order)

1. `internal/epub/recomposer_test.go` - Tests first
2. `internal/epub/recomposer.go` - Recomposer interface + EPUBRecomposer

### Test Cases

**Recomposer Tests:**
- Creates valid EPUB archive from Book struct
- `mimetype` is first entry in archive (uncompressed, Store method)
- `META-INF/container.xml` present and valid
- All spine documents included in output
- All resources preserved (images, CSS, fonts)
- Translated content correctly inserted into XHTML nodes
- Spine document count equals input

### Acceptance Criteria

- [ ] Tests written BEFORE implementation
- [ ] `EPUBRecomposer.Recompose()` creates valid EPUB archive
- [ ] `mimetype` is first entry (uncompressed)
- [ ] `META-INF/container.xml` present and valid
- [ ] All spine documents included with translated content
- [ ] All resources preserved (images, CSS, fonts)
- [ ] Spine document count equals input (zero missing)
- [ ] All tests pass: `env -u GOROOT go test ./internal/epub/...`
- [ ] `gofmt -w -s .` shows no changes
- [ ] `golangci-lint run` passes

---

## Development Phase 5: Pipeline Orchestration (TDD)

### Description

Implement pipeline orchestrator using TDD with mock dependencies. This phase wires together all components and provides the main entry point for the translation workflow.

### TDD Workflow

| Step | Phase | Action |
|------|-------|--------|
| 5.1 | RED | Write `orchestrator_test.go` with mocks for all dependencies |
| 5.2 | GREEN | Implement `errors.go`, `types.go`, `orchestrator.go` |
| 5.3 | REFACTOR | Review error handling |

### Files to Create (in order)

1. `internal/pipeline/orchestrator_test.go` - Tests first (with mocks)
2. `internal/pipeline/errors.go` - StageError type
3. `internal/pipeline/types.go` - RunConfig, RunResult, Stage enum
4. `internal/pipeline/orchestrator.go` - Orchestrator struct

### Test Cases (with Mocks)

**Orchestrator Tests:**
- Successful full pipeline run
- Extraction failure returns `StageError{Stage: StageExtract}`
- Parse failure returns `StageError{Stage: StageParse}`
- Segment failure returns `StageError{Stage: StageSegment}`
- Translation failure returns `StageError{Stage: StageTranslate}`
- Recompose failure returns `StageError{Stage: StageRecompose}`
- TM cache hit skips NMT translation call
- Context cancellation stops pipeline

### Acceptance Criteria

- [ ] Tests written BEFORE implementation (with mocks)
- [ ] `Orchestrator.Run()` executes stages: extract -> parse -> segment -> translate -> recompose
- [ ] `StageError` returned with correct stage on failure
- [ ] Exit code mapping works (ParseSegmentationFailed=3, ProviderFailed=4, etc.)
- [ ] TM cache hit skips NMT translation call
- [ ] All tests pass: `env -u GOROOT go test ./internal/pipeline/...`
- [ ] Test coverage >= 80%
- [ ] `gofmt -w -s .` shows no changes
- [ ] `golangci-lint run` passes

---

## Development Phase 6: CLI Integration & End-to-End Testing (TDD)

### Description

Wire orchestrator into CLI and create integration tests. This phase completes the pipeline by connecting it to the existing command execution layer.

### TDD Workflow

| Step | Phase | Action |
|------|-------|--------|
| 6.1 | RED | Write `tests/integration/pipeline_test.go`, create fixture EPUBs |
| 6.2 | GREEN | Wire `commandexec/execute.go`, add mock NMT server |
| 6.3 | REFACTOR | Review dependency injection |

### Files to Create (in order)

1. `tests/integration/pipeline_test.go` - Tests first
2. `tests/integration/fixtures/simple.epub` - Simple fixture with paragraphs
3. `tests/integration/fixtures/inline_markup.epub` - Fixture with inline formatting
4. `tests/integration/fixtures/complex.epub` - Fixture with mixed content
5. `tests/integration/mock_nmt.go` - Mock NMT server for deterministic tests

### Files to Modify

- `internal/commandexec/execute.go` - Wire pipeline orchestrator

### Test Cases

**Integration Tests:**
- Simple EPUB with paragraphs translates successfully
- EPUB with inline markup preserves placeholders
- Invalid EPUB returns proper error and exit code
- Exit codes match expected values for each failure mode
- Output EPUB exists at expected path
- Output EPUB has same spine document count as input

### Acceptance Criteria

- [ ] Integration tests written BEFORE CLI wiring
- [ ] `tomeshift run --input <fixture.epub> --output-dir ./out` succeeds
- [ ] Output EPUB exists at expected path
- [ ] Output EPUB has same spine document count as input
- [ ] All paragraphs contain translated text (mock NMT output)
- [ ] Placeholder restoration ratio >= 99.5%
- [ ] Zero missing spine documents
- [ ] Integration tests pass: `env -u GOROOT go test ./tests/integration/...`
- [ ] `just check` passes (fmt + lint + test)
- [ ] Exit codes match expected values

---

## Verification Commands

```bash
# Unit tests
env -u GOROOT go test ./internal/epub/...
env -u GOROOT go test ./internal/segment/...
env -u GOROOT go test ./internal/pipeline/...

# Integration tests
env -u GOROOT go test ./tests/integration/...

# Full validation
just check

# Manual end-to-end run
just run ./books/source.epub ./runs

# EPUB validation (if epubcheck available)
epubcheck ./runs/<run-id>/translated.epub
```

---

## Files Summary

### Documentation Files

- `docs/rfcs/0004-epub-pipeline.md` - Design proposal with options and decision
- `docs/implementation/epub-pipeline.md` - This implementation plan

### New Implementation Files

**internal/epub/**
- `doc.go`, `errors.go`, `types.go`
- `extractor.go`, `extractor_test.go`
- `parser.go`, `parser_test.go`
- `xhtml.go`, `xhtml_test.go`
- `recomposer.go`, `recomposer_test.go`

**internal/segment/**
- `doc.go`, `errors.go`, `types.go`
- `normalizer.go`, `normalizer_test.go`
- `hasher.go`, `hasher_test.go`
- `placeholder.go`, `placeholder_test.go`
- `segmenter.go`, `segmenter_test.go`

**internal/pipeline/**
- `doc.go`, `errors.go`, `types.go`
- `orchestrator.go`, `orchestrator_test.go`

**tests/integration/**
- `pipeline_test.go`
- `mock_nmt.go`
- `fixtures/simple.epub`
- `fixtures/inline_markup.epub`
- `fixtures/complex.epub`

### Files to Modify

- `go.mod` - Add `golang.org/x/net/html`
- `internal/commandexec/execute.go` - Wire pipeline

---

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Broken EPUB formatting | Placeholder tokenization + strict QA + epubcheck |
| Placeholder restoration failures | Fuzz testing, explicit error on mismatch |
| XHTML edge cases | Use `golang.org/x/net/html` (handles malformed HTML) |
| Large EPUB memory pressure | V1 accepts this; interfaces allow streaming in V2 |
| Complex nested inline markup | Clear precedence rules, edge-case fixtures |

---

## Progress Tracking

### Phase 1: Project Setup
- [ ] Started
- [ ] Completed

### Phase 2: EPUB Extraction & Parsing
- [ ] Started
- [ ] Tests written
- [ ] Implementation complete
- [ ] Refactored

### Phase 3: Text Segmentation & Placeholders
- [ ] Started
- [ ] Tests written
- [ ] Implementation complete
- [ ] Refactored

### Phase 4: EPUB Recomposition
- [ ] Started
- [ ] Tests written
- [ ] Implementation complete
- [ ] Refactored

### Phase 5: Pipeline Orchestration
- [ ] Started
- [ ] Tests written
- [ ] Implementation complete
- [ ] Refactored

### Phase 6: CLI Integration & E2E Testing
- [ ] Started
- [ ] Tests written
- [ ] Implementation complete
- [ ] Refactored

### Final Validation
- [ ] All unit tests pass
- [ ] All integration tests pass
- [ ] `just check` passes
- [ ] Manual E2E run succeeds
