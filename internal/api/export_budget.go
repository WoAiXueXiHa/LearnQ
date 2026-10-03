package api

import (
	"errors"
	"io"
)

var errExportTooLarge = errors.New("article export exceeds 256 MiB limit")

// One export per API process bounds aggregate temporary-file and compression
// work on the target small server. Callers receive an explicit retry response.
var exportSlots = make(chan struct{}, 1)

// Each export has separate aggregate raw and ZIP budgets; compression cannot
// hide an arbitrarily large logical archive or exhaust the temporary disk.
type exportBudget struct{ remaining int64 }
type exportWriter struct {
	writer io.Writer
	budget *exportBudget
}

func (w exportWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.budget.remaining {
		return 0, errExportTooLarge
	}
	n, err := w.writer.Write(data)
	w.budget.remaining -= int64(n)
	return n, err
}
