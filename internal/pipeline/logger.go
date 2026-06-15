package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const logsFileName = "logs.ndjson"

// NewRunLogger builds a structured logger that writes newline-delimited JSON to
// logs.ndjson inside outputDir. The returned closer flushes and closes the file.
func NewRunLogger(outputDir, level string) (*zap.Logger, func() error, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("unable to create log dir %q, err: %w", outputDir, err)
	}

	file, err := os.Create(filepath.Join(outputDir, logsFileName))
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create log file, err: %w", err)
	}

	lvl := zapcore.InfoLevel
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = zapcore.InfoLevel
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(file),
		lvl,
	)

	closer := func() error {
		_ = zap.New(core).Sync()
		return file.Close()
	}

	return zap.New(core), closer, nil
}
