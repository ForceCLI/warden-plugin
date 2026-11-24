package orgdata

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type contractObject struct {
	Fields map[string]struct {
		ReferenceTo string `json:"referenceTo"`
	} `json:"fields"`
	ChildRelationships map[string]string `json:"childRelationships"`
}

// membersOf returns the field names and child relationship names of an
// object, and for each lookup field the name of its parent relationship:
// the field name with __r in place of __c.
func membersOf(object contractObject) []string {
	var members []string
	for name, field := range object.Fields {
		members = append(members, name)
		if field.ReferenceTo != "" {
			members = append(members, strings.TrimSuffix(name, "__c")+"__r")
		}
	}
	for name := range object.ChildRelationships {
		members = append(members, name)
	}
	return members
}

var packageNamePattern = regexp.MustCompile(`\bwdn_\w+__(c|r)\b`)

// readSourceTokens returns the package API names in the Go files of the
// module that are not tests, sorted and without duplicates.
func readSourceTokens(t *testing.T, directory string) []string {
	t.Helper()
	var tokens []string
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tokens = append(tokens, packageNamePattern.FindAllString(string(content), -1)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(tokens)
	return slices.Compact(tokens)
}

func TestWardenBaseContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "warden-base-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Objects map[string]contractObject `json:"objects"`
	}
	if err := json.Unmarshal(content, &contract); err != nil {
		t.Fatal(err)
	}
	sourceTokens := readSourceTokens(t, "..")
	if len(sourceTokens) == 0 {
		t.Fatal("found no package API names in the source")
	}
	contractTokens := map[string]bool{}
	for name, object := range contract.Objects {
		contractTokens[name] = true
		for _, member := range membersOf(object) {
			contractTokens[member] = true
		}
	}
	usageTokens := map[string]bool{}
	for _, usage := range CLIContractUsage {
		usageTokens[usage.Object] = true
		for _, member := range usage.Members {
			usageTokens[member] = true
		}
	}

	t.Run("recognizes every package API name in the source", func(t *testing.T) {
		for _, token := range sourceTokens {
			if !contractTokens[token] {
				t.Errorf("%s is missing from the pinned contract", token)
			}
		}
	})

	t.Run("pins every listed field and relationship to its owning object", func(t *testing.T) {
		for _, usage := range CLIContractUsage {
			object, found := contract.Objects[usage.Object]
			if !found {
				t.Errorf("%s is not in the contract", usage.Object)
				continue
			}
			members := membersOf(object)
			for _, member := range usage.Members {
				if !slices.Contains(members, member) {
					t.Errorf("%s.%s is not in the contract", usage.Object, member)
				}
			}
		}
	})

	t.Run("lists every package API name used by the source", func(t *testing.T) {
		for _, token := range sourceTokens {
			if !usageTokens[token] {
				t.Errorf("%s is missing from CLIContractUsage", token)
			}
		}
	})
}
