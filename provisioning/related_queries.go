package provisioning

import (
	"fmt"

	"github.com/ForceCLI/warden-plugin/core"
)

const (
	matchQueryChunkSize = 200
	matchQueryMaxLength = 18_000
)

// MatchQueryBatches splits the values of an IN list so that no query has
// more than 200 values or more than 18,000 characters.  prefix is the
// query text before the list.  A value that does not fit in a query by
// itself is passed to onOversizedValue and left out; with a nil
// onOversizedValue it is an error.
func MatchQueryBatches(values []string, prefix string, onOversizedValue func(value string)) ([][]string, error) {
	var batches [][]string
	var current []string
	prefixLength := jsLength(prefix)
	// The 1 is the closing parenthesis.
	currentLength := prefixLength + 1
	for _, value := range values {
		renderedLength := jsLength("'" + core.Esc(value) + "'")
		if prefixLength+1+renderedLength > matchQueryMaxLength {
			if onOversizedValue == nil {
				return nil, fmt.Errorf("A lookup value exceeds the %d-character query budget.", matchQueryMaxLength)
			}
			onOversizedValue(value)
			continue
		}
		nextLength := currentLength + renderedLength
		if len(current) > 0 {
			nextLength++
		}
		if len(current) > 0 && (len(current) == matchQueryChunkSize || nextLength > matchQueryMaxLength) {
			batches = append(batches, current)
			current = nil
			currentLength = prefixLength + 1
		}
		current = append(current, value)
		if len(current) > 1 {
			currentLength++
		}
		currentLength += renderedLength
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches, nil
}
