// Package testprofile provisions disposable private directories for SQLite tests.
package testprofile

import "testing"

func Dir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if err := Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
