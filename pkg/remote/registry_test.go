package remote

import (
	"testing"
)

func TestRegisterAndListTypes(t *testing.T) {
	mockFactory := func(name string, options map[string]any, resolve Resolver) (Provider, error) {
		return nil, nil
	}

	Register("mock_test_type", mockFactory)

	types := ListTypes()
	found := false
	for _, typeName := range types {
		if typeName == "mock_test_type" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected 'mock_test_type' to be registered, got %v", types)
	}
}
