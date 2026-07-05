package migrations

import "testing"

func TestMigrationVersion10Registered(t *testing.T) {
	all := All()
	found := false
	for _, m := range all {
		if m.Version == 10 {
			found = true
			if m.Source != "00010_noop_git_import_fields.go" {
				t.Errorf("expected source to be '00010_noop_git_import_fields.go', got %q", m.Source)
			}
			break
		}
	}
	if !found {
		t.Error("migration version 10 is not registered")
	}
}
