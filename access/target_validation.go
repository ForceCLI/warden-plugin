package access

import (
	"strings"

	"github.com/ForceCLI/warden-plugin/core"
	"github.com/ForceCLI/warden-plugin/sfapi"
)

type setupEntityDefinition struct {
	objectName   string
	nameField    string
	notFoundCode string
}

var setupEntityDefinitions = map[string]setupEntityDefinition{
	TypeApexClass:        {objectName: "ApexClass", nameField: "Name", notFoundCode: "errorApexClassNotFound"},
	TypeVfPage:           {objectName: "ApexPage", nameField: "Name", notFoundCode: "errorVisualforcePageNotFound"},
	TypeCustomPermission: {objectName: "CustomPermission", nameField: "DeveloperName", notFoundCode: "errorCustomPermissionNotFound"},
}

func describeObject(conn sfapi.Conn, sobjectType string) (*sfapi.DescribeResult, error) {
	described, err := conn.Describe(sobjectType)
	if err != nil {
		return nil, newError("errorObjectNotFound", []string{sobjectType}, err)
	}
	return described, nil
}

func validateObjectTarget(conn sfapi.Conn, target string) (*ValidatedTarget, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, newError("errorInvalidTarget", []string{target}, nil)
	}
	described, err := describeObject(conn, trimmed)
	if err != nil {
		return nil, err
	}
	return &ValidatedTarget{Type: TypeObject, TargetName: described.Name, SobjectType: described.Name}, nil
}

func validateFieldTarget(conn sfapi.Conn, target string) (*ValidatedTarget, error) {
	trimmed := strings.TrimSpace(target)
	parts := strings.Split(trimmed, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, newError("errorFieldTargetMustBeQualified", []string{target}, nil)
	}
	inputObjectAPIName, inputFieldAPIName := parts[0], parts[1]
	described, err := describeObject(conn, inputObjectAPIName)
	if err != nil {
		return nil, err
	}
	fieldMap := map[string]string{}
	for _, describedField := range described.Fields {
		fieldMap[strings.ToLower(describedField.Name)] = describedField.Name
	}
	canonicalFieldAPIName := fieldMap[strings.ToLower(inputFieldAPIName)]
	if canonicalFieldAPIName == "" {
		return nil, newError("errorFieldNotFound", []string{inputObjectAPIName, inputFieldAPIName}, nil)
	}
	return &ValidatedTarget{
		Type:         TypeField,
		TargetName:   described.Name + "." + canonicalFieldAPIName,
		SobjectType:  described.Name,
		FieldAPIName: canonicalFieldAPIName,
	}, nil
}

func validateSetupEntityTarget(conn sfapi.Conn, targetType, target string) (*ValidatedTarget, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, newError("errorInvalidTarget", []string{target}, nil)
	}
	definition := setupEntityDefinitions[targetType]
	rows, err := queryAll(conn, "SELECT Id, "+definition.nameField+" FROM "+definition.objectName+" WHERE "+definition.nameField+" = '"+core.Esc(trimmed)+"' LIMIT 1")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[0] == nil {
		return nil, newError(definition.notFoundCode, []string{trimmed}, nil)
	}
	row := rows[0]
	return &ValidatedTarget{
		Type:          targetType,
		TargetName:    coalesce(opt(row, definition.nameField), lit(trimmed)),
		SetupEntityID: text(row, "Id"),
	}, nil
}

func validateTabTarget(conn sfapi.Conn, target string) (*ValidatedTarget, error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, newError("errorInvalidTarget", []string{target}, nil)
	}
	rows, err := queryAll(conn, "SELECT DurableId, Name FROM TabDefinition WHERE DurableId = '"+core.Esc(trimmed)+"' LIMIT 1")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[0] == nil {
		return nil, newError("errorTabNotFound", []string{trimmed}, nil)
	}
	return &ValidatedTarget{Type: TypeTab, TargetName: coalesce(opt(rows[0], "DurableId"), lit(trimmed))}, nil
}

func validateRecordTypeTarget(conn sfapi.Conn, target string) (*ValidatedTarget, error) {
	trimmed := strings.TrimSpace(target)
	parts := strings.Split(trimmed, ".")
	if trimmed == "" ||
		(strings.HasPrefix(trimmed, "012") && !strings.Contains(trimmed, ".")) ||
		len(parts) != 2 ||
		parts[0] == "" || parts[1] == "" {
		return nil, newError("errorRecordTypeTargetMustBeQualified", []string{target}, nil)
	}
	inputSobjectType, inputDeveloperName := parts[0], parts[1]
	if strings.ToLower(inputDeveloperName) == "master" {
		return nil, newError("errorMasterRecordTypeUnsupported", []string{trimmed}, nil)
	}
	rows, err := queryAll(conn, strings.Join([]string{
		"SELECT Id, SobjectType, DeveloperName, Name, IsActive",
		"FROM RecordType",
		"WHERE SobjectType = '" + core.Esc(inputSobjectType) + "'",
		"AND DeveloperName = '" + core.Esc(inputDeveloperName) + "'",
		"LIMIT 2",
	}, " "))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, newError("errorRecordTypeNotFound", []string{trimmed}, nil)
	}
	if len(rows) > 1 {
		return nil, newError("errorRecordTypeAmbiguous", []string{trimmed}, nil)
	}
	row := rows[0]
	if !isTrue(row, "IsActive") {
		return nil, newError("errorRecordTypeInactive", []string{trimmed}, nil)
	}
	sobjectType, developerName, id := text(row, "SobjectType"), text(row, "DeveloperName"), text(row, "Id")
	if sobjectType == "" || developerName == "" || id == "" {
		return nil, newError("errorRecordTypeNotFound", []string{trimmed}, nil)
	}
	return &ValidatedTarget{
		Type:         TypeRecordType,
		TargetName:   sobjectType + "." + developerName,
		SobjectType:  sobjectType,
		RecordTypeID: id,
	}, nil
}
