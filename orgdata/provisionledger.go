package orgdata

import (
	"slices"
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

// ProvisionLedgerOptions are the inputs of WriteProvisionLedger.
type ProvisionLedgerOptions struct {
	State  *CaptureState
	Output ProvisionResult
	// PersonasDoc is the persona document provisioning ran from, or nil.
	PersonasDoc *core.Object
	RunID       string
	Warn        func(message string)
}

// WriteProvisionLedger creates a ledger row for each captured grant.  A
// row that cannot be saved produces a warning.  The only error returned is
// that of the query for persona ids, which runs when the ledger has the
// field wdn_Persona__c.
func WriteProvisionLedger(conn sfapi.Conn, options ProvisionLedgerOptions) error {
	personaNames := []string{}
	for _, user := range options.Output.Users {
		for _, persona := range user.Personas {
			if !slices.Contains(personaNames, persona) {
				personaNames = append(personaNames, persona)
			}
		}
	}
	ledgerFields := DescribeObjectFields(conn, WardenLedgerObject)
	var personaIDsByName *OrderedMap[string]
	if ledgerFields.Has("wdn_Persona__c") && len(personaNames) > 0 {
		var err error
		if personaIDsByName, err = ReadPersonaIDs(conn, personaNames); err != nil {
			return err
		}
		unresolved := []string{}
		for _, name := range personaNames {
			if !personaIDsByName.Has(name) {
				unresolved = append(unresolved, name)
			}
		}
		if len(unresolved) > 0 {
			options.Warn("Warden org ledger provenance is unavailable for persona(s): " + strings.Join(unresolved, ", ") + ".")
		}
	}
	ledgerRows := BuildLedgerRows(LedgerRowsOptions{
		Grants:             options.State.Grants,
		Result:             options.Output,
		PersonasDoc:        options.PersonasDoc,
		LabelsByTypeAndID:  options.State.LabelsByTypeAndID,
		ReferenceIDsByType: options.State.ReferenceIDsByType,
		PersonaIDsByName:   personaIDsByName,
		RunID:              options.RunID,
	})
	WriteBatch(conn, WardenLedgerObject, LedgerRecords(ledgerRows), options.Warn, "ledger")
	return nil
}
