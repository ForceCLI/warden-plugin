package lifecycle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
	"github.com/ForceCLI/warden-plugin/sfapi/sfapitest"
)

// A scenario is one run of a use case against an org with fixed answers.
// The files under testdata/golden hold what the upstream JavaScript
// library (@syntax-syllogism/warden-core 0.7.0) produced for the same
// scenario: the queries it made, the record changes it sent, its plan,
// its results and its rendered text and CSV.  The source field of a
// scenario names the upstream test its data was taken from.
type scenario struct {
	Source   string          `json:"source"`
	Command  string          `json:"command"`
	Options  json.RawMessage `json:"options"`
	Describe []sfapi.Field   `json:"describe"`
	Queries  []struct {
		Contains string         `json:"contains"`
		Records  []sfapi.Record `json:"records"`
	} `json:"queries"`
	Saves []struct {
		Operation string             `json:"operation"`
		Sobject   string             `json:"sobject"`
		Results   []sfapi.SaveResult `json:"results"`
	} `json:"saves"`
	Apply bool `json:"apply"`
}

var defaultUserFields = []sfapi.Field{
	{Name: "Username", Createable: true, Updateable: true, Filterable: true, ExternalID: true},
	{Name: "FederationIdentifier", Createable: true, Updateable: true, Filterable: true, ExternalID: true},
	{Name: "IsFrozen", Createable: true, Updateable: true, Filterable: true},
	{Name: "IsActive", Createable: true, Updateable: true, Filterable: true},
}

func scenarioConn(sc scenario) *sfapitest.Conn {
	conn := sfapitest.New()
	fields := sc.Describe
	if fields == nil {
		fields = defaultUserFields
	}
	conn.DescribeFields("User", fields...)
	for _, answer := range sc.Queries {
		records := answer.Records
		if records == nil {
			records = []sfapi.Record{}
		}
		conn.Answer(answer.Contains, records...)
	}
	queued := map[string][][]sfapi.SaveResult{}
	for _, save := range sc.Saves {
		key := save.Operation + ":" + save.Sobject
		queued[key] = append(queued[key], save.Results)
	}
	conn.OnSave = func(change sfapitest.Change) ([]sfapi.SaveResult, error) {
		key := change.Operation + ":" + change.Sobject
		if len(queued[key]) > 0 {
			results := queued[key][0]
			queued[key] = queued[key][1:]
			return results, nil
		}
		count := len(change.Records)
		if change.Operation == "delete" {
			count = len(change.IDs)
		}
		results := make([]sfapi.SaveResult, count)
		for i := range results {
			results[i] = sfapi.SaveResult{ID: "saved", Success: true}
		}
		return results, nil
	}
	return conn
}

type scenarioOptions struct{ *core.Object }

func (o scenarioOptions) text(key string) string {
	value, _ := o.String(key)
	return value
}

func (o scenarioOptions) flag(key string) bool {
	value, _ := o.Get(key)
	return value == true
}

func (o scenarioOptions) targets() TargetOptions {
	return TargetOptions{
		User:             o.text("user"),
		UsersPath:        o.text("usersPath"),
		UsersDoc:         o.Object.Object("usersDoc"),
		ExternalID:       o.text("externalId"),
		InputFormat:      o.text("inputFormat"),
		CsvListDelimiter: o.text("csvListDelimiter"),
	}
}

func (o scenarioOptions) strip() StripOptions {
	options := StripOptions{
		TargetOptions:     o.targets(),
		Personas:          o.Strings("personas"),
		AllPersonas:       o.flag("allPersonas"),
		PersonasPath:      o.text("personasPath"),
		PersonasDoc:       o.Object.Object("personasDoc"),
		PersonaSource:     o.text("personaSource"),
		Freeze:            o.flag("freeze"),
		Deactivate:        o.flag("deactivate"),
		NoFreeze:          o.flag("noFreeze"),
		NoDeactivate:      o.flag("noDeactivate"),
		KeepPermsets:      o.flag("keepPermsets"),
		KeepPermsetGroups: o.flag("keepPermsetGroups"),
		KeepPublicGroups:  o.flag("keepPublicGroups"),
		KeepQueues:        o.flag("keepQueues"),
		KeepLicenses:      o.flag("keepLicenses"),
	}
	if entries, ok := o.List("ledgerTargets"); ok {
		options.LedgerTargets = []LedgerTarget{}
		for _, item := range entries {
			entry := scenarioOptions{item.(*core.Object)}
			options.LedgerTargets = append(options.LedgerTargets, LedgerTarget{UserID: entry.text("userId"), Persona: entry.text("persona"), TargetID: entry.text("targetId")})
		}
	}
	return options
}

func (o scenarioOptions) diff() DiffOptions {
	return DiffOptions{
		Mode:             o.text("mode"),
		Verify:           o.flag("verify"),
		User:             o.text("user"),
		Against:          o.text("against"),
		UsersPath:        o.text("usersPath"),
		UsersDoc:         o.Object.Object("usersDoc"),
		PersonasPath:     o.text("personasPath"),
		PersonasDoc:      o.Object.Object("personasDoc"),
		ExternalID:       o.text("externalId"),
		InputFormat:      o.text("inputFormat"),
		CsvListDelimiter: o.text("csvListDelimiter"),
	}
}

// rendered writes a result with its text and CSV forms.
func rendered(result any, human, csv string) json.RawMessage {
	var object jsonObject
	object.field("result", result)
	object.field("human", human)
	object.field("csv", csv)
	encoded, _ := object.bytes()
	return encoded
}

func runScenario(t *testing.T, sc scenario, conn sfapi.Conn, out *jsonObject) error {
	parsed, err := core.ParseJSON(sc.Options)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	options := scenarioOptions{parsed.(*core.Object)}
	switch sc.Command {
	case "freeze", "unfreeze":
		plan, apply := PlanFreeze, ApplyFreeze
		if sc.Command == "unfreeze" {
			plan, apply = PlanUnfreeze, ApplyUnfreeze
		}
		planned, err := plan(conn, options.targets())
		if err != nil {
			return err
		}
		out.field("plan", planned)
		dryRun := &Result{Summary: SummarizeLifecycle(planned.Users), Users: planned.Users}
		out.field("preview", rendered(dryRun, RenderLifecycleResult(dryRun, RenderMessages), RenderLifecycleCsv(dryRun)))
		if sc.Apply {
			result, err := apply(conn, planned)
			if err != nil {
				return err
			}
			out.field("applied", rendered(result, RenderLifecycleResult(result, RenderMessages), RenderLifecycleCsv(result)))
		}
	case "strip":
		stripOptions := options.strip()
		if err := ValidateStripOptions(stripOptions); err != nil {
			return err
		}
		planned, err := PlanStrip(conn, stripOptions)
		if err != nil {
			return err
		}
		out.field("plan", planned)
		out.field("preview", rendered(planned.Preview, RenderLifecycleResult(planned.Preview, RenderMessages), RenderStripCsv(planned.Preview)))
		if sc.Apply {
			result, err := ApplyStrip(conn, planned)
			if err != nil {
				return err
			}
			out.field("applied", rendered(result, RenderLifecycleResult(result, RenderMessages), RenderStripCsv(result)))
		}
	case "restore":
		planned, err := PlanRestore(conn, RestoreOptions{SnapshotPath: options.text("snapshotPath"), SnapshotDoc: options.Object.Object("snapshotDoc")})
		if err != nil {
			return err
		}
		out.field("plan", planned)
		out.field("preview", rendered(planned.Preview, RenderLifecycleResult(planned.Preview, RenderMessages), RenderRestoreCsv(planned.Preview)))
		if sc.Apply {
			result, err := ApplyRestore(conn, planned)
			if err != nil {
				return err
			}
			out.field("applied", rendered(result, RenderLifecycleResult(result, RenderMessages), RenderRestoreCsv(result)))
		}
	case "snapshot":
		file, err := RunSnapshot(conn, SnapshotOptions{TargetOptions: options.targets(), Org: options.text("org")})
		if err != nil {
			return err
		}
		out.field("snapshot", file)
		directory := filepath.Join(t.TempDir(), "nested")
		jsonPath, csvPath := filepath.Join(directory, "snapshot.json"), filepath.Join(directory, "snapshot.csv")
		for _, path := range []string{jsonPath, csvPath} {
			if err := WriteSnapshotFile(path, file); err != nil {
				return err
			}
		}
		for _, written := range []struct{ key, path string }{{"jsonFile", jsonPath}, {"csvFile", csvPath}} {
			content, err := os.ReadFile(written.path)
			if err != nil {
				return err
			}
			out.field(written.key, string(content))
		}
		for _, written := range []struct{ key, path string }{{"readJson", jsonPath}, {"readCsv", csvPath}} {
			read, err := ReadSnapshotFile(written.path)
			if err != nil {
				return err
			}
			out.field(written.key, read)
		}
		result := SnapshotToLifecycleResult(file)
		out.field("lifecycle", rendered(result, RenderLifecycleResult(result, RenderMessages), RenderSnapshotCsv(result)))
	case "readSnapshot":
		file, err := ReadSnapshotFile(options.text("path"))
		if err != nil {
			return err
		}
		out.field("snapshot", file)
		encoded, err := MarshalSnapshot(file)
		if err != nil {
			return err
		}
		out.field("json", string(encoded))
		out.field("csv", SerializeSnapshotCsv(file))
	case "diff":
		run, err := RunDiff(conn, options.diff())
		if err != nil {
			return err
		}
		if run.Result == nil {
			out.field("verdicts", run)
			out.field("human", RenderUserConformanceHuman(run.Verdicts, RenderMessages))
			out.field("csv", RenderUserConformanceCsv(run.Verdicts))
			return nil
		}
		lookup := RenderMessagesFor("diff")
		out.field("result", run)
		out.field("human", RenderUserDiffHuman(run.Result, lookup, false))
		out.field("humanVerbose", RenderUserDiffHuman(run.Result, lookup, true))
		out.field("csv", RenderUserDiffCsv(run.Result))
		verdicts := VerifyUserDiff(run.Result, RenderMessages)
		var verified jsonObject
		verified.field("verdicts", verdicts)
		verified.field("human", RenderUserConformanceHuman(verdicts, RenderMessages))
		verified.field("csv", RenderUserConformanceCsv(verdicts))
		encoded, _ := verified.bytes()
		out.field("verified", json.RawMessage(encoded))
	default:
		t.Fatalf("unknown command %q", sc.Command)
	}
	return nil
}

func changeJSON(change sfapitest.Change) json.RawMessage {
	var object jsonObject
	object.field("operation", change.Operation)
	object.field("sobject", change.Sobject)
	if change.Operation == "delete" {
		object.field("ids", change.IDs)
	} else {
		object.field("records", change.Records)
	}
	object.field("allOrNone", change.AllOrNone)
	encoded, _ := object.bytes()
	return encoded
}

func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		gotLine, wantLine := "<end>", "<end>"
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if gotLine != wantLine {
			start := max(i-6, 0)
			return "line " + strings.TrimSpace(strings.Join([]string{string(rune('0' + (i+1)/1000%10)), string(rune('0' + (i+1)/100%10)), string(rune('0' + (i+1)/10%10)), string(rune('0' + (i+1)%10))}, "")) +
				"\ncontext:\n" + strings.Join(wantLines[start:min(i, len(wantLines))], "\n") + "\n got: " + gotLine + "\nwant: " + wantLine
		}
	}
	return ""
}

func TestScenariosMatchUpstream(t *testing.T) {
	paths, err := filepath.Glob("testdata/scenarios/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no scenarios: %v", err)
	}
	Now = func() time.Time { return time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC) }
	defer func() { Now = time.Now }()
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var sc scenario
			if err := json.Unmarshal(content, &sc); err != nil {
				t.Fatal(err)
			}
			conn := scenarioConn(sc)
			var out jsonObject
			if err := runScenario(t, sc, conn, &out); err != nil {
				var failure jsonObject
				if wardenError, ok := core.AsWardenError(err); ok {
					failure.field("name", wardenError.Name)
					failure.field("code", wardenError.Code)
				} else {
					failure.field("name", "Error")
				}
				failure.field("message", err.Error())
				encoded, _ := failure.bytes()
				out.field("error", json.RawMessage(encoded))
			}
			out.field("queries", append([]string{}, conn.Queries...))
			changes := []json.RawMessage{}
			for _, change := range conn.Changes {
				changes = append(changes, changeJSON(change))
			}
			out.field("changes", changes)
			compact, err := out.bytes()
			if err != nil {
				t.Fatal(err)
			}
			var indented bytes.Buffer
			if err := json.Indent(&indented, compact, "", "  "); err != nil {
				t.Fatal(err)
			}
			indented.WriteByte('\n')
			want, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := indented.String(); got != string(want) {
				t.Errorf("output differs from upstream at %s", firstDifference(got, string(want)))
			}
		})
	}
}
