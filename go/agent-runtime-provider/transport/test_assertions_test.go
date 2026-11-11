package transport

import "testing"

const testJSONSchemaTypeString = "string"

func requireNoError(t *testing.T, err error, format string) {
	t.Helper()
	if err != nil {
		t.Fatalf(format, err)
	}
}

func requireCondition(t *testing.T, condition bool, format string, args ...interface{}) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func requireEqual[T comparable](t *testing.T, got T, want T, format string, args ...interface{}) {
	t.Helper()
	if got != want {
		t.Fatalf(format, args...)
	}
}
