package templates

import "github.com/dagger/dagger/codegen/generator"

// FormatTypeFunc is an implementation of generator.FormatTypeFuncs interface
// to format GraphQL type into Golang.
type FormatTypeFunc struct{}

func (f *FormatTypeFunc) FormatKindList(representation string) string {
	representation = "[]" + representation
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarString(representation string) string {
	representation += "string"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarInt(representation string) string {
	representation += "int"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarFloat(representation string) string {
	representation += "float"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarBoolean(representation string) string {
	representation += "bool"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarDefault(representation string, refName string, input bool) string {
	if alias, ok := generator.CustomScalar[refName]; ok && input {
		representation += "*" + alias
	} else {
		representation += refName
	}

	return representation
}

func (f *FormatTypeFunc) FormatKindObject(representation string, refName string) string {
	representation += formatName(refName)
	return representation
}

func (f *FormatTypeFunc) FormatKindInputObject(representation string, refName string) string {
	representation += formatName(refName)
	return representation
}

func (f *FormatTypeFunc) FormatKindEnum(representation string, refName string) string {
	representation += refName
	return representation
}
