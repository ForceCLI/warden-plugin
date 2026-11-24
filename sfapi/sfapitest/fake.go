// Package sfapitest provides an in-memory org connection for tests.
package sfapitest

import (
	"fmt"
	"strings"

	"github.com/ForceCLI/warden-plugin/sfapi"
)

// Change is one call that changes records.
type Change struct {
	// Operation is "create", "update" or "delete".
	Operation string
	Sobject   string
	Records   []sfapi.Record
	IDs       []string
	AllOrNone bool
}

// Conn is an sfapi.Conn that answers from functions and tables set by a
// test and records what it was asked.
type Conn struct {
	URL     string
	Version string
	// Queries holds every SOQL text passed to Query, in order.
	Queries []string
	// OnQuery answers a query.  When it is nil, or returns handled false,
	// the first entry of Results whose key is a substring of the query
	// answers it; a query no entry matches returns no records.
	OnQuery func(soql string) (records []sfapi.Record, handled bool, err error)
	Results []QueryAnswer
	// More maps a nextRecordsUrl to the page it returns.
	More map[string]*sfapi.QueryResult
	// Describes maps an sObject name in lower case to its description.
	Describes map[string]*sfapi.DescribeResult
	// Changes holds every create, update and delete call, in order.
	Changes []Change
	// OnSave returns the results of a change.  When it is nil every
	// record succeeds, with ids "<sobject>-<n>" for created records.
	OnSave func(change Change) ([]sfapi.SaveResult, error)
	// OnMetadataRead and OnMetadataList answer Metadata API calls.
	OnMetadataRead func(metadataType string, fullNames []string) ([]sfapi.Record, error)
	OnMetadataList func(metadataType string) ([]sfapi.Record, error)
	created        int
}

// QueryAnswer is the records for queries that contain a text.
type QueryAnswer struct {
	Contains string
	Records  []sfapi.Record
	// Next, when set, is returned as nextRecordsUrl with Done false.
	Next string
}

// New returns a connection with no data.
func New() *Conn {
	return &Conn{URL: "https://example.my.salesforce.com", Version: "67.0", More: map[string]*sfapi.QueryResult{}, Describes: map[string]*sfapi.DescribeResult{}}
}

// Answer adds records for queries that contain text.
func (c *Conn) Answer(contains string, records ...sfapi.Record) *Conn {
	c.Results = append(c.Results, QueryAnswer{Contains: contains, Records: records})
	return c
}

// Field returns a describe field that can be created, updated and
// filtered.
func Field(name, fieldType string) sfapi.Field {
	return sfapi.Field{Name: name, Type: fieldType, Createable: true, Updateable: true, Filterable: true}
}

// DescribeFields sets the description of an sObject.
func (c *Conn) DescribeFields(sobject string, fields ...sfapi.Field) *Conn {
	c.Describes[strings.ToLower(sobject)] = &sfapi.DescribeResult{Name: sobject, Fields: fields}
	return c
}

func (c *Conn) InstanceURL() string { return c.URL }

func (c *Conn) APIVersion() string { return c.Version }

func (c *Conn) Query(soql string) (*sfapi.QueryResult, error) {
	c.Queries = append(c.Queries, soql)
	if c.OnQuery != nil {
		records, handled, err := c.OnQuery(soql)
		if err != nil {
			return nil, err
		}
		if handled {
			return &sfapi.QueryResult{TotalSize: len(records), Done: true, Records: records}, nil
		}
	}
	for _, answer := range c.Results {
		if strings.Contains(soql, answer.Contains) {
			return &sfapi.QueryResult{TotalSize: len(answer.Records), Done: answer.Next == "", NextRecordsURL: answer.Next, Records: answer.Records}, nil
		}
	}
	return &sfapi.QueryResult{Done: true, Records: []sfapi.Record{}}, nil
}

func (c *Conn) QueryMore(nextRecordsURL string) (*sfapi.QueryResult, error) {
	if page, ok := c.More[nextRecordsURL]; ok {
		return page, nil
	}
	return nil, fmt.Errorf("no page at %s", nextRecordsURL)
}

func (c *Conn) Describe(sobject string) (*sfapi.DescribeResult, error) {
	if described, ok := c.Describes[strings.ToLower(sobject)]; ok {
		return described, nil
	}
	return nil, &sfapi.APIError{Status: 404, ErrorCode: "NOT_FOUND", Message: "The requested resource does not exist"}
}

func (c *Conn) save(change Change) ([]sfapi.SaveResult, error) {
	c.Changes = append(c.Changes, change)
	if c.OnSave != nil {
		return c.OnSave(change)
	}
	count := len(change.Records)
	if change.Operation == "delete" {
		count = len(change.IDs)
	}
	results := make([]sfapi.SaveResult, count)
	for i := range results {
		results[i] = sfapi.SaveResult{Success: true}
		switch change.Operation {
		case "create":
			c.created++
			results[i].ID = fmt.Sprintf("%s-%d", change.Sobject, c.created)
		case "update":
			results[i].ID, _ = change.Records[i]["Id"].(string)
		case "delete":
			results[i].ID = change.IDs[i]
		}
	}
	return results, nil
}

func (c *Conn) Create(sobject string, records []sfapi.Record, allOrNone bool) ([]sfapi.SaveResult, error) {
	return c.save(Change{Operation: "create", Sobject: sobject, Records: records, AllOrNone: allOrNone})
}

func (c *Conn) Update(sobject string, records []sfapi.Record, allOrNone bool) ([]sfapi.SaveResult, error) {
	return c.save(Change{Operation: "update", Sobject: sobject, Records: records, AllOrNone: allOrNone})
}

func (c *Conn) Delete(sobject string, ids []string, allOrNone bool) ([]sfapi.SaveResult, error) {
	return c.save(Change{Operation: "delete", Sobject: sobject, IDs: ids, AllOrNone: allOrNone})
}

func (c *Conn) MetadataRead(metadataType string, fullNames []string) ([]sfapi.Record, error) {
	if c.OnMetadataRead != nil {
		return c.OnMetadataRead(metadataType, fullNames)
	}
	return nil, nil
}

func (c *Conn) MetadataList(metadataType string) ([]sfapi.Record, error) {
	if c.OnMetadataList != nil {
		return c.OnMetadataList(metadataType)
	}
	return nil, nil
}

var _ sfapi.Conn = (*Conn)(nil)
