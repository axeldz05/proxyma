package protocol

import (
	"errors"
	"fmt"
)

// Error wording shared by every validation layer. The schema validator (ValidateValue,
// typed any) and the admin validator (uischema.validateAdminParam, string) stay separate
// on purpose; only the user-facing text is centralized here.

// ParamTypeError reports a parameter whose value does not match its declared type.
func ParamTypeError(paramName, expected string) error {
	return fmt.Errorf("invalid type for parameter '%s': expected %s", paramName, expected)
}

// ParamOptionError reports a value outside the parameter's Options set.
func ParamOptionError(paramName, value string, options []string) error {
	return fmt.Errorf("invalid value for parameter '%s': %q not in options %v", paramName, value, options)
}

// MissingParamError reports a required argument that arrived empty.
func MissingParamError(name string) error {
	return fmt.Errorf("missing %s parameter", name)
}

// ErrEmptyPipelineID is returned by every layer that requires a pipeline identifier.
var ErrEmptyPipelineID = errors.New("pipeline ID cannot be empty")
