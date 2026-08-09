package clusters

import (
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

func TestMemberResourcesIncludesCreatorAndAvoidsDuplicates(t *testing.T) {
	createdAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	cluster := &model.Cluster{Id: "cluster", CreatorId: "creator", CreatedAt: createdAt, Creator: &model.User{Email: "creator@example.com", Name: "Creator", Status: model.UserStatusACTIVE}}
	members := []model.ClusterMember{
		{ClusterId: "cluster", UserId: "creator", Permission: model.ClusterPermissionVIEWER, CreatedAt: createdAt.Add(time.Minute), User: &model.User{Email: "creator@example.com"}},
		{ClusterId: "cluster", UserId: "operator", Permission: model.ClusterPermissionOPERATOR, CreatedAt: createdAt.Add(2 * time.Minute), User: &model.User{Email: "operator@example.com", Name: "Operator", Status: model.UserStatusACTIVE}},
	}

	result := memberResources(cluster, members)
	if len(result) != 2 {
		t.Fatalf("member count=%d, want 2: %#v", len(result), result)
	}
	if result[0].UserID != "creator" || result[0].Permission != model.ClusterPermissionOWNER {
		t.Fatalf("creator was not represented as owner: %#v", result[0])
	}
	if result[1].UserID != "operator" || result[1].Permission != model.ClusterPermissionOPERATOR {
		t.Fatalf("ordinary member changed unexpectedly: %#v", result[1])
	}
	if result[0].Email != "creator@example.com" || result[1].Email != "operator@example.com" {
		t.Fatalf("member user summaries missing: %#v", result)
	}
}
