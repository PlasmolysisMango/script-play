package engine

import (
	"encoding/json"
	"os"
	"testing"

	"script-play/internal/domain"
)

// The printed rule "不能抽取自己所在楼层" is enforced by matching a clue's floor
// with the floor of the seat's room. Every storey has more cards than the six
// seats can take, so the guard must hold for the whole round rather than only
// fall back when the deck runs dry.
func TestSearchSkipsOwnFloor(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/K3-大疫/script.json")
	if err != nil {
		t.Skipf("K3 不在磁盘上：%v", err)
	}
	var sc domain.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	var seats []domain.Seat
	floors := map[string]string{}
	for i, r := range sc.Roles {
		if r.Floor == "" {
			t.Fatalf("角色 %s 没有楼层", r.Name)
		}
		id := "seat" + string(rune('0'+i+1))
		seats = append(seats, domain.Seat{
			ID: id, RoomID: "r", RoleID: r.ID, Kind: domain.SeatHuman, DisplayName: r.Name,
		})
		floors[id] = r.Floor
	}

	g := domain.NewGame("g-floor", "r", sc.Meta.Title, seats)
	g.Status = domain.RoomRunning
	a := NewActor(g, &sc, nil, nil, nil, ActorConfig{})

	var search *domain.Phase
	for i := range sc.Phases {
		if sc.Phases[i].Type == domain.PhaseSearch {
			g.PhaseIndex = i
			search = &sc.Phases[i]
			break
		}
	}
	if search == nil {
		t.Fatal("K3 没有搜证阶段")
	}

	for _, seat := range seats {
		got := a.availableCluesLocked(seat, search)
		if len(got) == 0 {
			t.Fatalf("%s 一张可抽的卡都没有", seat.DisplayName)
		}
		for _, c := range got {
			if c.Floor != "" && c.Floor == floors[seat.ID] {
				t.Fatalf("%s 住 %s，却抽到了同楼层的「%s」",
					seat.DisplayName, floors[seat.ID], c.Title)
			}
		}
	}
}

// The pick list is only half the rule: a seat can still name a card directly, so
// the claim itself has to refuse a card from the seat's own storey while the
// storeys still hold cards nobody has taken.
func TestClaimRefusesOwnFloor(t *testing.T) {
	raw, err := os.ReadFile("../../../scripts/K3-大疫/script.json")
	if err != nil {
		t.Skipf("K3 不在磁盘上：%v", err)
	}
	var sc domain.Script
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	var seats []domain.Seat
	for i, r := range sc.Roles {
		seats = append(seats, domain.Seat{
			ID: "seat" + string(rune('0'+i+1)), RoomID: "r", RoleID: r.ID,
			Kind: domain.SeatHuman, DisplayName: r.Name,
		})
	}

	g := domain.NewGame("g-claim", "r", sc.Meta.Title, seats)
	g.Status = domain.RoomRunning
	a := NewActor(g, &sc, nil, nil, nil, ActorConfig{})

	var search *domain.Phase
	for i := range sc.Phases {
		if sc.Phases[i].Type == domain.PhaseSearch {
			g.PhaseIndex = i
			search = &sc.Phases[i]
			break
		}
	}
	if search == nil {
		t.Fatal("K3 没有搜证阶段")
	}

	// 尤娜住二楼，s01 是她自己楼层的卡。先把她那两张一楼抽掉，走到「任意楼层」那一步。
	seat := seats[0]
	if seat.RoleID != "r_you_na" {
		t.Fatalf("首个角色变了：%s", seat.RoleID)
	}
	for _, id := range []string{"f01", "f02"} {
		if err := a.claimClue(seat, id, search); err != nil {
			t.Fatalf("抽 %s 失败：%v", id, err)
		}
	}
	if err := a.claimClue(seat, "s01", search); err == nil {
		t.Fatal("住在二楼的尤娜抽走了二楼的 s01")
	}
}
