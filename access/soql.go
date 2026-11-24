package access

import (
	"errors"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// queryAll returns the records of every page of a query.
func queryAll(conn sfapi.Conn, soql string) ([]sfapi.Record, error) {
	rows := []sfapi.Record{}
	result, err := conn.Query(soql)
	if err != nil {
		return nil, err
	}
	rows = append(rows, result.Records...)
	for !result.Done && result.NextRecordsURL != "" {
		result, err = conn.QueryMore(result.NextRecordsURL)
		if err != nil {
			return nil, err
		}
		rows = append(rows, result.Records...)
	}
	return rows, nil
}

const defaultChunkSize = 250

func chunkValues(values []string, chunkSize int) ([][]string, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunkSize must be greater than 0")
	}
	return core.Batch(values, chunkSize), nil
}

// queryAllInChunks runs one query for each 250 values and returns the
// records of all of them, in order.
func queryAllInChunks(conn sfapi.Conn, values []string, buildSoql func(chunk []string) string) ([]sfapi.Record, error) {
	rows := []sfapi.Record{}
	if len(values) == 0 {
		return rows, nil
	}
	chunks, err := chunkValues(values, defaultChunkSize)
	if err != nil {
		return nil, err
	}
	for _, chunk := range chunks {
		page, err := queryAll(conn, buildSoql(chunk))
		if err != nil {
			return nil, err
		}
		rows = append(rows, page...)
	}
	return rows, nil
}

// quotedList writes ids as a quoted list without escaping, as the muting
// queries of the field and object resolvers do.
func quotedList(ids []string) string {
	text := ""
	for i, id := range ids {
		if i > 0 {
			text += ","
		}
		text += "'" + id + "'"
	}
	return text
}
