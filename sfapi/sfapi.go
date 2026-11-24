// Package sfapi makes the Salesforce API requests the warden commands
// need: SOQL queries, describes, record changes and Metadata API reads.
package sfapi

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Record is a Salesforce record or any other JSON object of the API.
type Record = map[string]any

// QueryResult is one page of a query.
type QueryResult struct {
	TotalSize      int      `json:"totalSize"`
	Done           bool     `json:"done"`
	NextRecordsURL string   `json:"nextRecordsUrl"`
	Records        []Record `json:"records"`
}

// SaveError is one error of a record change.
type SaveError struct {
	StatusCode string   `json:"statusCode"`
	Message    string   `json:"message"`
	Fields     []string `json:"fields"`
}

// SaveResult is the outcome of the change of one record.
type SaveResult struct {
	ID      string      `json:"id"`
	Success bool        `json:"success"`
	Errors  []SaveError `json:"errors"`
}

// Field is a field of a describe result.
type Field struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Createable    bool   `json:"createable"`
	Updateable    bool   `json:"updateable"`
	Filterable    bool   `json:"filterable"`
	ExternalID    bool   `json:"externalId"`
	Unique        *bool  `json:"unique"`
	CaseSensitive *bool  `json:"caseSensitive"`
	// Readable is not part of every describe response.
	Readable *bool  `json:"readable"`
	Raw      Record `json:"-"`
}

// RecordTypeInfo is a record type of a describe result.
type RecordTypeInfo struct {
	RecordTypeID string `json:"recordTypeId"`
	Available    *bool  `json:"available"`
	IsPersonType bool   `json:"isPersonType"`
}

// DescribeResult is the description of an sObject.
type DescribeResult struct {
	Name            string           `json:"name"`
	Queryable       *bool            `json:"queryable"`
	Fields          []Field          `json:"fields"`
	RecordTypeInfos []RecordTypeInfo `json:"recordTypeInfos"`
}

// Conn is a connection to an org.
type Conn interface {
	// Query returns the first page of a query.
	Query(soql string) (*QueryResult, error)
	// QueryMore returns the page at a nextRecordsUrl.
	QueryMore(nextRecordsURL string) (*QueryResult, error)
	Describe(sobject string) (*DescribeResult, error)
	// Create, Update and Delete change up to 200 records in one request
	// and return one result for each record, in order.  Larger lists are
	// sent in several requests.
	Create(sobject string, records []Record, allOrNone bool) ([]SaveResult, error)
	Update(sobject string, records []Record, allOrNone bool) ([]SaveResult, error)
	Delete(sobject string, ids []string, allOrNone bool) ([]SaveResult, error)
	// MetadataRead reads components of one type through the Metadata API.
	MetadataRead(metadataType string, fullNames []string) ([]Record, error)
	// MetadataList lists the components of one type.
	MetadataList(metadataType string) ([]Record, error)
	InstanceURL() string
	APIVersion() string
}

// APIError is an error response of the REST API.
type APIError struct {
	Status    int
	ErrorCode string
	Message   string
	Fields    []string
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return e.ErrorCode + ": " + e.Message
	}
	return e.Message
}

// Client is a Conn over HTTP with a bearer access token.
type Client struct {
	instanceURL string
	accessToken string
	apiVersion  string
	http        *http.Client
}

// NewClient returns a connection to the org at instanceURL.  apiVersion
// is written like "67.0".
func NewClient(instanceURL, accessToken, apiVersion string) *Client {
	return &Client{
		instanceURL: strings.TrimRight(instanceURL, "/"),
		accessToken: accessToken,
		apiVersion:  strings.TrimPrefix(apiVersion, "v"),
		http:        &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *Client) InstanceURL() string { return c.instanceURL }

func (c *Client) APIVersion() string { return c.apiVersion }

func (c *Client) base() string {
	return c.instanceURL + "/services/data/v" + c.apiVersion
}

func (c *Client) do(method, target string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		var failures []struct {
			ErrorCode string   `json:"errorCode"`
			Message   string   `json:"message"`
			Fields    []string `json:"fields"`
		}
		if json.Unmarshal(content, &failures) == nil && len(failures) > 0 {
			return &APIError{Status: response.StatusCode, ErrorCode: failures[0].ErrorCode, Message: failures[0].Message, Fields: failures[0].Fields}
		}
		return &APIError{Status: response.StatusCode, Message: fmt.Sprintf("%s: %s", response.Status, strings.TrimSpace(string(content)))}
	}
	if out == nil || len(content) == 0 {
		return nil
	}
	return json.Unmarshal(content, out)
}

func (c *Client) Query(soql string) (*QueryResult, error) {
	var result QueryResult
	if err := c.do(http.MethodGet, c.base()+"/query?q="+url.QueryEscape(soql), nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) QueryMore(nextRecordsURL string) (*QueryResult, error) {
	var result QueryResult
	if err := c.do(http.MethodGet, c.instanceURL+nextRecordsURL, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) Describe(sobject string) (*DescribeResult, error) {
	var raw struct {
		DescribeResult
		RawFields []Record `json:"fields"`
	}
	var content json.RawMessage
	if err := c.do(http.MethodGet, c.base()+"/sobjects/"+url.PathEscape(sobject)+"/describe", nil, &content); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &raw.DescribeResult); err != nil {
		return nil, err
	}
	var fields struct {
		Fields []Record `json:"fields"`
	}
	if err := json.Unmarshal(content, &fields); err == nil {
		for i := range raw.DescribeResult.Fields {
			if i < len(fields.Fields) {
				raw.DescribeResult.Fields[i].Raw = fields.Fields[i]
			}
		}
	}
	return &raw.DescribeResult, nil
}

const collectionSize = 200

func withType(sobject string, records []Record) []Record {
	typed := make([]Record, len(records))
	for i, record := range records {
		copied := Record{"attributes": Record{"type": sobject}}
		for key, value := range record {
			if key != "attributes" {
				copied[key] = value
			}
		}
		typed[i] = copied
	}
	return typed
}

// saveOne changes one record with a single-record request and reports a
// refused change as a failed result, as the collections resource does.
func (c *Client) saveOne(method, sobject string, record Record) (SaveResult, error) {
	target := c.base() + "/sobjects/" + url.PathEscape(sobject)
	id, _ := record["Id"].(string)
	var body any
	switch method {
	case http.MethodPost:
		body = withoutKeys(record, "attributes")
	case http.MethodPatch:
		target += "/" + url.PathEscape(id)
		body = withoutKeys(record, "attributes", "Id")
	case http.MethodDelete:
		target += "/" + url.PathEscape(id)
	}
	var created struct {
		ID string `json:"id"`
	}
	err := c.do(method, target, body, &created)
	if err == nil {
		if created.ID != "" {
			id = created.ID
		}
		return SaveResult{ID: id, Success: true, Errors: []SaveError{}}, nil
	}
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.ErrorCode != "" && apiError.Status < 500 && apiError.Status != http.StatusUnauthorized {
		return SaveResult{Success: false, Errors: []SaveError{{StatusCode: apiError.ErrorCode, Message: apiError.Message, Fields: apiError.Fields}}}, nil
	}
	return SaveResult{}, err
}

func withoutKeys(record Record, keys ...string) Record {
	copied := Record{}
	for key, value := range record {
		copied[key] = value
	}
	for _, key := range keys {
		delete(copied, key)
	}
	return copied
}

// saveEach changes the records one request at a time.  With allOrNone it
// stops at the first record that is refused; the records before it stay
// changed, because single-record requests cannot be rolled back.
func (c *Client) saveEach(method, sobject string, records []Record, allOrNone bool) ([]SaveResult, error) {
	results := make([]SaveResult, 0, len(records))
	for _, record := range records {
		result, err := c.saveOne(method, sobject, record)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
		if allOrNone && !result.Success {
			break
		}
	}
	return results, nil
}

// collectionsMissing reports an error that says the server has no
// collections resource.
func collectionsMissing(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.Status == http.StatusNotFound
}

// saveCollection changes records through the collections resource, 200 in
// each request.  One record is changed with a single-record request.  A
// server without the collections resource gets one request for each record.
func (c *Client) saveCollection(method, sobject string, records []Record, allOrNone bool) ([]SaveResult, error) {
	if len(records) == 1 {
		return c.saveEach(method, sobject, records, allOrNone)
	}
	var results []SaveResult
	for start := 0; start < len(records); start += collectionSize {
		end := min(start+collectionSize, len(records))
		var page []SaveResult
		body := Record{"allOrNone": allOrNone, "records": withType(sobject, records[start:end])}
		if err := c.do(method, c.base()+"/composite/sobjects", body, &page); err != nil {
			if start == 0 && collectionsMissing(err) {
				return c.saveEach(method, sobject, records, allOrNone)
			}
			return nil, err
		}
		results = append(results, page...)
	}
	return results, nil
}

func (c *Client) Create(sobject string, records []Record, allOrNone bool) ([]SaveResult, error) {
	return c.saveCollection(http.MethodPost, sobject, records, allOrNone)
}

func (c *Client) Update(sobject string, records []Record, allOrNone bool) ([]SaveResult, error) {
	return c.saveCollection(http.MethodPatch, sobject, records, allOrNone)
}

func (c *Client) Delete(sobject string, ids []string, allOrNone bool) ([]SaveResult, error) {
	byID := func() ([]SaveResult, error) {
		records := make([]Record, len(ids))
		for i, id := range ids {
			records[i] = Record{"Id": id}
		}
		// The sObject of a record is not part of a delete by id; the
		// single-record resource needs it.
		return c.saveEach(http.MethodDelete, sobject, records, allOrNone)
	}
	if len(ids) == 1 {
		return byID()
	}
	var results []SaveResult
	for start := 0; start < len(ids); start += collectionSize {
		end := min(start+collectionSize, len(ids))
		var page []SaveResult
		target := fmt.Sprintf("%s/composite/sobjects?ids=%s&allOrNone=%t", c.base(), url.QueryEscape(strings.Join(ids[start:end], ",")), allOrNone)
		if err := c.do(http.MethodDelete, target, nil, &page); err != nil {
			if start == 0 && collectionsMissing(err) {
				return byID()
			}
			return nil, err
		}
		results = append(results, page...)
	}
	return results, nil
}

func xmlEscape(text string) string {
	var out bytes.Buffer
	xml.EscapeText(&out, []byte(text))
	return out.String()
}

func (c *Client) soap(body string) ([]byte, error) {
	envelope := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns="http://soap.sforce.com/2006/04/metadata">` +
		`<soapenv:Header><SessionHeader><sessionId>` + xmlEscape(c.accessToken) + `</sessionId></SessionHeader></soapenv:Header>` +
		`<soapenv:Body>` + body + `</soapenv:Body></soapenv:Envelope>`
	request, err := http.NewRequest(http.MethodPost, c.instanceURL+"/services/Soap/m/"+c.apiVersion, strings.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	request.Header.Set("SOAPAction", `""`)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		if fault := soapFault(content); fault != "" {
			return nil, &APIError{Status: response.StatusCode, Message: fault}
		}
		return nil, &APIError{Status: response.StatusCode, Message: response.Status}
	}
	return content, nil
}

type xmlNode struct {
	XMLName  xml.Name
	Content  string    `xml:",chardata"`
	Children []xmlNode `xml:",any"`
}

func soapFault(content []byte) string {
	var envelope xmlNode
	if xml.Unmarshal(content, &envelope) != nil {
		return ""
	}
	var find func(node xmlNode) string
	find = func(node xmlNode) string {
		if node.XMLName.Local == "faultstring" {
			return strings.TrimSpace(node.Content)
		}
		for _, child := range node.Children {
			if text := find(child); text != "" {
				return text
			}
		}
		return ""
	}
	return find(envelope)
}

// toValue converts an element of a Metadata API response: an element
// without children is its text, "true" and "false" as booleans, and an
// element with children is a Record in which a repeated child is a list.
func toValue(node xmlNode) any {
	if len(node.Children) == 0 {
		text := strings.TrimSpace(node.Content)
		switch text {
		case "true":
			return true
		case "false":
			return false
		}
		return text
	}
	record := Record{}
	for _, child := range node.Children {
		value := toValue(child)
		name := child.XMLName.Local
		switch existing := record[name].(type) {
		case nil:
			if _, present := record[name]; present {
				record[name] = []any{existing, value}
			} else {
				record[name] = value
			}
		case []any:
			record[name] = append(existing, value)
		default:
			record[name] = []any{existing, value}
		}
	}
	return record
}

// results returns the elements named name anywhere below the response
// element of a SOAP body.
func soapResults(content []byte, name string) ([]Record, error) {
	var envelope xmlNode
	if err := xml.Unmarshal(content, &envelope); err != nil {
		return nil, err
	}
	var records []Record
	var walk func(node xmlNode)
	walk = func(node xmlNode) {
		if node.XMLName.Local == name {
			if record, ok := toValue(node).(Record); ok {
				records = append(records, record)
			}
			return
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(envelope)
	return records, nil
}

func (c *Client) MetadataRead(metadataType string, fullNames []string) ([]Record, error) {
	var body strings.Builder
	body.WriteString("<readMetadata><type>" + xmlEscape(metadataType) + "</type>")
	for _, name := range fullNames {
		body.WriteString("<fullNames>" + xmlEscape(name) + "</fullNames>")
	}
	body.WriteString("</readMetadata>")
	content, err := c.soap(body.String())
	if err != nil {
		return nil, err
	}
	return soapResults(content, "records")
}

func (c *Client) MetadataList(metadataType string) ([]Record, error) {
	body := "<listMetadata><queries><type>" + xmlEscape(metadataType) + "</type></queries><asOfVersion>" + xmlEscape(c.apiVersion) + "</asOfVersion></listMetadata>"
	content, err := c.soap(body)
	if err != nil {
		return nil, err
	}
	return soapResults(content, "result")
}

// Upsert creates or updates up to 200 records in one request, matching
// existing records on an external id field.  Larger lists are sent in
// several requests.
func (c *Client) Upsert(sobject string, records []Record, externalIDField string, allOrNone bool) ([]SaveResult, error) {
	var results []SaveResult
	target := c.base() + "/composite/sobjects/" + url.PathEscape(sobject) + "/" + url.PathEscape(externalIDField)
	for start := 0; start < len(records); start += collectionSize {
		end := min(start+collectionSize, len(records))
		var page []SaveResult
		body := Record{"allOrNone": allOrNone, "records": withType(sobject, records[start:end])}
		if err := c.do(http.MethodPatch, target, body, &page); err != nil {
			return nil, err
		}
		results = append(results, page...)
	}
	return results, nil
}

// UserID returns the Id of the user the access token belongs to.
func (c *Client) UserID() (string, error) {
	var info struct {
		UserID string `json:"user_id"`
	}
	if err := c.do(http.MethodGet, c.instanceURL+"/services/oauth2/userinfo", nil, &info); err != nil {
		return "", err
	}
	return info.UserID, nil
}
