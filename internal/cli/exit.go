package cli

import (
	"errors"
	"fmt"

	"github.com/Harry10799/ktrx/internal/analyzer"
)

type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }

// ExitCode returns a stable process exit code for KTRX errors.
// 0 means the diagnosis completed without actionable findings;
// 1 means command/configuration/execution error;
// 2 means a confirmed failure or warning;
// 3 means the result is unknown or incomplete.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}

func verdictExitError(verdict string) error {
	switch verdict {
	case "FAILED", "WARN":
		return &exitCodeError{code: 2, msg: "diagnostic completed with " + verdict + " findings"}
	case "UNKNOWN":
		return &exitCodeError{code: 3, msg: "diagnostic completed with an UNKNOWN verdict"}
	default:
		return nil
	}
}

func podReportsExitError(reports []*analyzer.PodReport) error {
	hasUnknown, hasWarning := false, false
	for _, report := range reports {
		if report == nil {
			continue
		}
		switch report.Verdict {
		case analyzer.Failed:
			return verdictExitError("FAILED")
		case analyzer.Unknown:
			hasUnknown = true
		case analyzer.Warning:
			hasWarning = true
		}
	}
	// Match the documented headline precedence: FAILED > UNKNOWN > WARN > HEALTHY.
	if hasUnknown {
		return verdictExitError("UNKNOWN")
	}
	if hasWarning {
		return verdictExitError("WARN")
	}
	return nil
}

func exitCodeErrorf(code int, format string, args ...any) error {
	return &exitCodeError{code: code, msg: fmt.Sprintf(format, args...)}
}
