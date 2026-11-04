package authz

import "testing"

func TestSelfSessionOperationIsExact(t *testing.T) {
	for _, tc := range []struct {
		path, method string
		want         bool
	}{
		{"/api/v1/admin/logout", "POST", true}, {"/admin/logout", "POST", true},
		{"/api/v1/admin/logout", "GET", false}, {"/api/v1/admin/logout/other", "POST", false},
		{"/api/v1/admin/users/1/logout", "POST", false}, {"", "POST", false},
	} {
		if got := IsAdminSelfSessionOperation(tc.path, tc.method); got != tc.want {
			t.Errorf("%s %s: got %v", tc.method, tc.path, got)
		}
	}
}
