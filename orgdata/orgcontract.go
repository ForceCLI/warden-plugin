package orgdata

// ContractObject names the fields and SOQL relationships of one package
// object that this program uses.
type ContractObject struct {
	Object  string
	Members []string
}

// CLIContractUsage lists the package fields and SOQL relationships this
// program uses, grouped by the object that owns them.
var CLIContractUsage = []ContractObject{
	{PersonaObject, []string{PersonaAPINameField, PersonaActiveField}},
	{PersonaComponentObject, []string{ComponentPersonaField, ComponentTypeField, ComponentReferenceField}},
	{PersonaAssignmentObject, []string{
		PersonaAssignmentUserField,
		PersonaAssignmentPersonaField,
		"wdn_Persona__r",
		PersonaAssignmentStatusField,
		"wdn_Revoked_On__c",
		"wdn_Revoked_By__c",
	}},
	{WardenLedgerObject, []string{
		"wdn_User__c",
		PersonaAssignmentPersonaField,
		"wdn_Persona__r",
		"wdn_Type__c",
		"wdn_Target_Id__c",
		"wdn_Target_Label__c",
		"wdn_Granted_On__c",
		"wdn_Granted_By_Run__c",
		"wdn_Revoked_On__c",
		"wdn_Revoked_By_Run__c",
	}},
	{WardenRunObject, []string{
		"wdn_Mode__c",
		"wdn_Scope__c",
		"wdn_Planned_Count__c",
		"wdn_Applied_Count__c",
		"wdn_Error_Count__c",
		"wdn_Unmanaged_Count__c",
		"wdn_Source_System__c",
		"wdn_Cli_Version__c",
		"wdn_Invoking_User__c",
		"wdn_Ci_Job__c",
		"wdn_Definition_Path__c",
		"wdn_Definition_Sha__c",
	}},
	{WardenItemObject, []string{
		WardenRunObject,
		"wdn_User_Key__c",
		"wdn_User_Id__c",
		"wdn_Category__c",
		"wdn_Action__c",
		"wdn_Status__c",
		"wdn_Detail__c",
		"wdn_Error__c",
	}},
}
