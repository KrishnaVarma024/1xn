package auth

import "testing"

func TestEveryPermissionHasRolesAndTheOwnerHasThemAll(t *testing.T) {
	if len(matrix) != len(AllPermissions) {
		t.Fatalf("matrix has %d permissions, AllPermissions %d", len(matrix), len(AllPermissions))
	}
	for _, p := range AllPermissions {
		if len(matrix[p]) == 0 {
			t.Errorf("%s: no role has it", p)
		}
		if !Can(Owner, p) {
			t.Errorf("the owner cannot %s", p)
		}
	}
	if Can("", Floor) || Can("chef", Floor) || Can(Cashier, Staff) || Can(Manager, Books) {
		t.Fatal("a role has a permission it should not")
	}
	if got := len(PermissionsOf(Cashier)); got != 4 {
		t.Fatalf("a cashier has %d permissions: %v", got, PermissionsOf(Cashier))
	}
}
