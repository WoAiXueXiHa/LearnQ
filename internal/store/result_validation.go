package store

import "errors"

// ResultValidationError indicates a rejected model result, not a transient
// database failure. Repeating the provider request is not an automatic repair.
type ResultValidationError struct{ Reason string }

func (e *ResultValidationError) Error() string { return "RESULT_VALIDATION_FAILED: " + e.Reason }
func invalidResult(reason string) error        { return &ResultValidationError{Reason: reason} }
func IsResultValidationError(err error) bool {
	var rejected *ResultValidationError
	return errors.As(err, &rejected)
}
