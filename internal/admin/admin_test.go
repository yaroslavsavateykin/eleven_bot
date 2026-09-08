package admin

import (
	"context"
	"group411/internal/db"
	"path/filepath"
	"testing"
)

func TestIdentityBinding(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Exec(`INSERT INTO groups VALUES(1,'test',-1,'UTC','test','','')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, owner  int64
		name, role string
	}{{10, 10, "owner", "admin"}, {11, 10, "owner", "member"}, {10, 10, "renamed", "admin"}, {12, 0, "owner", "member"}} {
		id, err := EnsureMember(d, 1, tc.id, tc.name, "", "", tc.owner)
		if err != nil {
			t.Fatal(err)
		}
		var role string
		if err = d.QueryRow("SELECT role FROM group_members WHERE user_id=?", id).Scan(&role); err != nil || role != tc.role {
			t.Fatal(role, err)
		}
	}
}
