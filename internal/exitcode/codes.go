package exitcode

const (
	// Success indicates command execution completed without errors.
	Success = 0
	// InvalidInputConfig indicates invalid CLI args or runtime configuration.
	InvalidInputConfig = 2
	// ParseSegmentationFailed indicates EPUB parse/segmentation stage failure.
	ParseSegmentationFailed = 3
	// ProviderFailed indicates translation provider call failures.
	ProviderFailed = 4
	// QAFailed indicates QA gate checks failed.
	QAFailed = 5
	// RebuildValidationFailed indicates EPUB rebuild/validation failure.
	RebuildValidationFailed = 6
)
