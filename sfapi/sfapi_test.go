package sfapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type request struct {
	method string
	path   string
	body   string
}

// server answers with the handler and records each request.
func server(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body string)) (*Client, *[]request) {
	t.Helper()
	var seen []request
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer token" && !strings.HasPrefix(r.URL.Path, "/services/Soap/") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seen = append(seen, request{method: r.Method, path: r.URL.RequestURI(), body: string(body)})
		handler(w, r, string(body))
	}))
	t.Cleanup(httpServer.Close)
	return NewClient(httpServer.URL+"/", "token", "v67.0"), &seen
}

func TestQueryAndQueryMore(t *testing.T) {
	client, seen := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.HasPrefix(r.URL.Path, "/services/data/v67.0/query/next") {
			io.WriteString(w, `{"totalSize":2,"done":true,"records":[{"Id":"2"}]}`)
			return
		}
		io.WriteString(w, `{"totalSize":2,"done":false,"nextRecordsUrl":"/services/data/v67.0/query/next-1","records":[{"Id":"1"}]}`)
	})
	first, err := client.Query("SELECT Id FROM User WHERE Name = 'a b'")
	if err != nil || first.Done || len(first.Records) != 1 || first.Records[0]["Id"] != "1" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := client.QueryMore(first.NextRecordsURL)
	if err != nil || !second.Done || second.Records[0]["Id"] != "2" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	if (*seen)[0].path != "/services/data/v67.0/query?q=SELECT+Id+FROM+User+WHERE+Name+%3D+%27a+b%27" {
		t.Errorf("query request = %s", (*seen)[0].path)
	}
}

func TestErrorResponseCarriesTheErrorCode(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `[{"errorCode":"MALFORMED_QUERY","message":"unexpected token"}]`)
	})
	_, err := client.Query("SELECT")
	apiError, ok := err.(*APIError)
	if !ok || apiError.ErrorCode != "MALFORMED_QUERY" || err.Error() != "MALFORMED_QUERY: unexpected token" {
		t.Errorf("error = %v", err)
	}
}

func TestDescribe(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		io.WriteString(w, `{"name":"User","queryable":true,"fields":[{"name":"Username","type":"string","filterable":true,"unique":true},{"name":"IsActive","type":"boolean"}],"recordTypeInfos":[{"recordTypeId":"012A","available":false}]}`)
	})
	described, err := client.Describe("User")
	if err != nil {
		t.Fatal(err)
	}
	if described.Name != "User" || len(described.Fields) != 2 || !described.Fields[0].Filterable || described.Fields[0].Unique == nil || !*described.Fields[0].Unique || described.Fields[1].Unique != nil {
		t.Errorf("describe = %+v", described)
	}
	if described.RecordTypeInfos[0].Available == nil || *described.RecordTypeInfos[0].Available {
		t.Errorf("record type = %+v", described.RecordTypeInfos[0])
	}
}

func TestOneRecordUsesTheSingleRecordResource(t *testing.T) {
	client, seen := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"001A","success":true,"errors":[]}`)
		case http.MethodPatch:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `[{"errorCode":"FIELD_INTEGRITY_EXCEPTION","message":"bad value","fields":["Name"]}]`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	created, err := client.Create("Account", []Record{{"Name": "Acme"}}, false)
	if err != nil || len(created) != 1 || !created[0].Success || created[0].ID != "001A" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	updated, err := client.Update("Account", []Record{{"Id": "001A", "Name": ""}}, false)
	if err != nil || updated[0].Success || updated[0].Errors[0].StatusCode != "FIELD_INTEGRITY_EXCEPTION" || !reflect.DeepEqual(updated[0].Errors[0].Fields, []string{"Name"}) {
		t.Fatalf("a refused update = %+v, %v", updated, err)
	}
	deleted, err := client.Delete("Account", []string{"001A"}, false)
	if err != nil || !deleted[0].Success || deleted[0].ID != "001A" {
		t.Fatalf("delete = %+v, %v", deleted, err)
	}
	want := []request{
		{http.MethodPost, "/services/data/v67.0/sobjects/Account", `{"Name":"Acme"}`},
		{http.MethodPatch, "/services/data/v67.0/sobjects/Account/001A", `{"Name":""}`},
		{http.MethodDelete, "/services/data/v67.0/sobjects/Account/001A", ""},
	}
	if !reflect.DeepEqual(*seen, want) {
		t.Errorf("requests = %+v", *seen)
	}
}

func TestSeveralRecordsUseTheCollectionsResource(t *testing.T) {
	client, seen := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		io.WriteString(w, `[{"id":"1","success":true,"errors":[]},{"success":false,"errors":[{"statusCode":"DUPLICATE_VALUE","message":"duplicate","fields":[]}]}]`)
	})
	results, err := client.Create("GroupMember", []Record{{"GroupId": "00G1", "UserOrGroupId": "005A"}, {"GroupId": "00G2", "UserOrGroupId": "005A"}}, false)
	if err != nil || len(results) != 2 || !results[0].Success || results[1].Success || results[1].Errors[0].StatusCode != "DUPLICATE_VALUE" {
		t.Fatalf("results = %+v, %v", results, err)
	}
	var body struct {
		AllOrNone bool     `json:"allOrNone"`
		Records   []Record `json:"records"`
	}
	if err := json.Unmarshal([]byte((*seen)[0].body), &body); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].path != "/services/data/v67.0/composite/sobjects" || body.AllOrNone || len(body.Records) != 2 || body.Records[0]["attributes"].(map[string]any)["type"] != "GroupMember" {
		t.Errorf("request = %+v", (*seen)[0])
	}
	if _, err := client.Delete("GroupMember", []string{"011A", "011B"}, true); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[1]; got.method != http.MethodDelete || got.path != "/services/data/v67.0/composite/sobjects?ids=011A%2C011B&allOrNone=true" {
		t.Errorf("delete request = %+v", got)
	}
	if _, err := client.Upsert("wdn_Persona__c", []Record{{"wdn_Api_Name__c": "Sales"}}, "wdn_Api_Name__c", false); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[2]; got.method != http.MethodPatch || got.path != "/services/data/v67.0/composite/sobjects/wdn_Persona__c/wdn_Api_Name__c" {
		t.Errorf("upsert request = %+v", got)
	}
}

func TestServerWithoutCollectionsGetsOneRequestForEachRecord(t *testing.T) {
	client, seen := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.Contains(r.URL.Path, "/composite/") {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not_found"}`)
			return
		}
		io.WriteString(w, `{"id":"new","success":true,"errors":[]}`)
	})
	results, err := client.Create("Account", []Record{{"Name": "A"}, {"Name": "B"}}, false)
	if err != nil || len(results) != 2 || !results[0].Success || !results[1].Success {
		t.Fatalf("results = %+v, %v", results, err)
	}
	if len(*seen) != 3 || (*seen)[1].path != "/services/data/v67.0/sobjects/Account" {
		t.Errorf("requests = %+v", *seen)
	}
}

func TestMetadataReadConvertsElements(t *testing.T) {
	client, seen := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		io.WriteString(w, `<?xml version="1.0"?><soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"><soapenv:Body><readMetadataResponse xmlns="http://soap.sforce.com/2006/04/metadata"><result>`+
			`<records xsi:type="Profile" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><fullName>Admin</fullName><recordTypeVisibilities><default>true</default><recordType>Account.Business</recordType><visible>true</visible></recordTypeVisibilities><recordTypeVisibilities><default>false</default><recordType>Account.Person</recordType><visible>false</visible></recordTypeVisibilities></records>`+
			`<records xsi:type="Profile" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><fullName>Standard</fullName></records>`+
			`</result></readMetadataResponse></soapenv:Body></soapenv:Envelope>`)
	})
	records, err := client.MetadataRead("Profile", []string{"Admin", "Standard & Co"})
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %+v, %v", records, err)
	}
	visibilities, ok := records[0]["recordTypeVisibilities"].([]any)
	if records[0]["fullName"] != "Admin" || !ok || len(visibilities) != 2 || visibilities[0].(Record)["visible"] != true || visibilities[1].(Record)["default"] != false {
		t.Errorf("first record = %+v", records[0])
	}
	got := (*seen)[0]
	if got.path != "/services/Soap/m/67.0" || !strings.Contains(got.body, "<sessionId>token</sessionId>") || !strings.Contains(got.body, "<fullNames>Standard &amp; Co</fullNames>") {
		t.Errorf("request = %+v", got)
	}
}

func TestSoapFaultIsTheError(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"><soapenv:Body><soapenv:Fault><faultcode>sf:INVALID_SESSION_ID</faultcode><faultstring>INVALID_SESSION_ID: Invalid Session ID</faultstring></soapenv:Fault></soapenv:Body></soapenv:Envelope>`)
	})
	if _, err := client.MetadataList("Profile"); err == nil || err.Error() != "INVALID_SESSION_ID: Invalid Session ID" {
		t.Errorf("error = %v", err)
	}
}

func TestUserID(t *testing.T) {
	client, seen := server(t, func(w http.ResponseWriter, r *http.Request, body string) {
		io.WriteString(w, `{"user_id":"005A","organization_id":"00D1"}`)
	})
	id, err := client.UserID()
	if err != nil || id != "005A" || (*seen)[0].path != "/services/oauth2/userinfo" {
		t.Errorf("UserID = %q, %v; request %+v", id, err, *seen)
	}
}
