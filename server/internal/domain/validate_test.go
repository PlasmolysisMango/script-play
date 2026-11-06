package domain_test

import (
	"testing"

	"script-play/internal/domain"
)

// baseScript is the smallest publishable script; each case mutates one clue.
func baseScript(clues ...domain.Clue) *domain.Script {
	if len(clues) == 0 {
		clues = []domain.Clue{{ID: "c1", Title: "告示", Content: "十点熄灯。"}}
	}
	return &domain.Script{
		SchemaVersion: 1,
		Meta:          domain.Meta{Title: "校验测试本", PlayerCount: domain.CountRange{Min: 2, Max: 2}},
		Truth:         domain.Truth{KillerRoleID: "r1"},
		Phases: []domain.Phase{
			{ID: "p1", Type: domain.PhaseReading, Name: "阅读剧本"},
			{ID: "p2", Type: domain.PhaseSearch, Name: "搜证", Round: 1, CluePool: []string{"c1"}, PerPlayerPick: 1},
			{ID: "p3", Type: domain.PhaseReveal, Name: "复盘"},
		},
		Roles: []domain.Role{
			{ID: "r1", Name: "甲", Private: domain.RolePrivate{Script: "甲的本"}},
			{ID: "r2", Name: "乙", Private: domain.RolePrivate{Script: "乙的本"}},
		},
		Clues: clues,
	}
}

func hasPath(errs []domain.ValidationError, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

func TestValidateAcceptsTheNewClueFields(t *testing.T) {
	sc := baseScript(
		domain.Clue{ID: "c1", Title: "后院", Content: "少了一把钳子。", GrantsItem: "钳子"},
		domain.Clue{ID: "c2", Title: "地窖", Content: "线芯裸露。", RequiresItems: []string{"钳子"}, UnlockedBy: []string{"c1"}},
		domain.Clue{ID: "c3", Title: "口袋", Content: "一张车票。", SubjectRoleID: "r2"},
		domain.Clue{ID: "c4", Title: "碎片", Content: "花瓶。", OwnerRoleID: "r1", Keywords: []string{"花瓶"}},
	)
	if errs := sc.Validate(); len(errs) > 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
}

func TestValidateRejectsBrokenClueLinks(t *testing.T) {
	cases := []struct {
		name  string
		clues []domain.Clue
		path  string
	}{
		{
			name:  "unknown subject role",
			clues: []domain.Clue{{ID: "c1", Title: "口袋", Content: "车票。", SubjectRoleID: "r9"}},
			path:  "clues[0].subjectRoleId",
		},
		{
			name: "subject is also the owner",
			clues: []domain.Clue{
				{ID: "c1", Title: "口袋", Content: "车票。", SubjectRoleID: "r1", OwnerRoleID: "r1"},
			},
			path: "clues[0].subjectRoleId",
		},
		{
			name:  "unknown unlockedBy",
			clues: []domain.Clue{{ID: "c1", Title: "地窖", Content: "线芯。", UnlockedBy: []string{"c9"}}},
			path:  "clues[0].unlockedBy",
		},
		{
			name:  "self reference",
			clues: []domain.Clue{{ID: "c1", Title: "地窖", Content: "线芯。", UnlockedBy: []string{"c1"}}},
			path:  "clues[0].unlockedBy",
		},
		{
			name:  "keyword without owner",
			clues: []domain.Clue{{ID: "c1", Title: "碎片", Content: "花瓶。", Keywords: []string{"花瓶"}}},
			path:  "clues[0].keywords",
		},
		{
			name:  "blank required item",
			clues: []domain.Clue{{ID: "c1", Title: "地窖", Content: "线芯。", RequiresItems: []string{" "}}},
			path:  "clues[0].requiresItems[0]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := baseScript(tc.clues...).Validate()
			if !hasPath(errs, tc.path) {
				t.Fatalf("expected an error at %s, got %v", tc.path, errs)
			}
		})
	}
}

// A search phase that turns revealOnFind on must still satisfy the base rules,
// and the flag itself may not break the engine's phase contract.
func TestValidateAcceptsPublicSearchPhase(t *testing.T) {
	sc := baseScript()
	sc.Phases[1].RevealOnFind = true
	if errs := sc.Validate(); len(errs) > 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
}
