package orgdata

import (
	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// PersonaImportRequestInput names the persona file to import and whether
// to delete the components the file does not list.
type PersonaImportRequestInput struct {
	Input string
	Prune bool
}

// PersonaImportPlan is a persona file that was read and its comparison
// with the active personas of the org.
type PersonaImportPlan struct {
	Document *core.Object `json:"document"`
	Diff     PersonaDiff  `json:"diff"`
	Prune    bool         `json:"prune"`
}

// PlanPersonaImportRequest reads the persona file and the active personas
// of the org and compares them.  It writes nothing.
func PlanPersonaImportRequest(conn sfapi.Conn, input PersonaImportRequestInput) (*PersonaImportPlan, error) {
	document, err := ReadPersonaFile(input.Input)
	if err != nil {
		return nil, err
	}
	org, err := ReadOrgPersonas(conn, true)
	if err != nil {
		return nil, err
	}
	return &PersonaImportPlan{Document: document, Diff: DiffPersonaDocuments(document, org.Document), Prune: input.Prune}, nil
}

// ApplyPersonaImportRequest writes the document of a plan to the org.
func ApplyPersonaImportRequest(conn sfapi.Conn, plan *PersonaImportPlan) (*PersonaPromotionResult, error) {
	return ImportOrgPersonas(conn, plan.Document, plan.Prune)
}
