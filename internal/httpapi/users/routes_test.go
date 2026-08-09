package users

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"goveto-edge/internal/storage/gen/model"
)

func TestUserFiltersRejectInvalidEnums(t *testing.T) {
	for _, query := range []string{"role=OWNER", "status=PENDING"} {
		req := httptest.NewRequest("GET", "/?"+query, nil)
		ctx := echo.New().NewContext(req, httptest.NewRecorder())
		if _, err := userFilters(ctx); err == nil {
			t.Fatalf("query %q should fail", query)
		}
	}
}

func TestValidateStatusChangeProtectsSelfAndFinalAdmin(t *testing.T) {
	admin := &model.User{Id: "admin", Role: model.UserRoleADMIN}
	if err := validateStatusChange("admin", admin, model.UserStatusDISABLED, 2); err == nil {
		t.Fatal("self-disable should fail")
	}
	if err := validateStatusChange("other", admin, model.UserStatusDISABLED, 1); err == nil {
		t.Fatal("disabling final active admin should fail")
	}
	if err := validateStatusChange("other", admin, model.UserStatusDISABLED, 2); err != nil {
		t.Fatalf("disabling one of multiple admins failed: %v", err)
	}
	if err := validateStatusChange("admin", admin, model.UserStatusACTIVE, 1); err != nil {
		t.Fatalf("enabling a user should not be blocked: %v", err)
	}
}

func TestPositiveIntBounds(t *testing.T) {
	if got := positiveInt("25", 50, 200); got != 25 {
		t.Fatalf("positiveInt valid = %d", got)
	}
	for _, value := range []string{"", "0", "201", "bad"} {
		if got := positiveInt(value, 50, 200); got != 50 {
			t.Fatalf("positiveInt(%q) = %d, want fallback", value, got)
		}
	}
}
