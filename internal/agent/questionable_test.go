package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/espn"
)

// backfield is the week-9 Jets case: a lead back listed questionable after
// missing the last weeks, ESPN projecting him a little above the backup
// who has carried the load meanwhile.
func backfield(leadProj float64) (*fakeESPN, *espn.Player, *espn.Player) {
	f, _ := scenario()
	lead := mk("Lead Back", "RB", espn.SlotBench, leadProj)
	lead.InjuryStatus = espn.InjuryQuestionable
	backup := mk("Backup Back", "RB", espn.SlotBench, 9)
	lead.ProTeamID, backup.ProTeamID = 31, 31
	// The lead back hasn't played the last three weeks; the backup scored
	// 19, 23 and 18 as the lead in them.
	var kept []espn.StatLine
	for _, st := range lead.Stats {
		if !(st.Weekly && !st.Projected && st.Week >= week-3) {
			kept = append(kept, st)
		}
	}
	lead.Stats = kept
	for i, st := range backup.Stats {
		if st.Weekly && !st.Projected {
			switch st.Week {
			case week - 3:
				backup.Stats[i].Points = 19
			case week - 2:
				backup.Stats[i].Points = 23
			case week - 1:
				backup.Stats[i].Points = 18
			}
		}
	}
	// A 12-point receiver holds FLEX, so only one of the two backs starts.
	flex := mk("Flex WR", "WR", espn.SlotBench, 12)
	base := f.league
	f.league = func() *espn.League {
		l := base()
		me := l.Teams[0]
		var r []*espn.Player
		for _, p := range me.Roster {
			if p.Name != "RB Scrub" && p.Name != "WR Scrub" { // make room
				r = append(r, p)
			}
		}
		for _, p := range []*espn.Player{lead, backup, flex} {
			c := *p
			c.OnTeamID = me.ID
			r = append(r, &c)
		}
		me.Roster = r
		return l
	}
	f.pool = nil
	return f, lead, backup
}

func starts(p *Plan, name string) bool {
	for _, pl := range p.Roster.Roster {
		if pl.Name == name {
			return espn.IsStartingSlot(p.Lineup.Slots[pl.ID])
		}
	}
	return false
}

func TestQuestionableLeadBackSitsForHisBackupOnACloseCall(t *testing.T) {
	f, lead, _ := backfield(10.9)
	a := testAgent(f)
	a.Cfg.IREligibleStatuses = []string{}
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if starts(p, "Lead Back") || !starts(p, "Backup Back") {
		t.Errorf("started the questionable lead back over his backup on a 2-point call")
	}
	// The projections shown stay ESPN's.
	if v := p.Values[lead.ID]; v.WeekProjection != 10.9 || v.PlayChance != 0.85 {
		t.Errorf("lead back projection %.2f, play chance %.2f", v.WeekProjection, v.PlayChance)
	}
	var backup *espn.Player
	for _, pl := range p.Roster.Roster {
		if pl.Name == "Backup Back" {
			backup = pl
		}
	}
	notes := strings.Join(p.Values[backup.ID].Notes, "; ")
	if !strings.Contains(notes, "Lead Back is questionable; if he sits, Backup Back takes over (20.0 a game in his recent games without Lead Back)") {
		t.Errorf("backup's notes: %q", notes)
	}
}

func TestQuestionableStarStillStartsOverAWeakBackup(t *testing.T) {
	f, _, _ := backfield(20)
	a := testAgent(f)
	a.Cfg.IREligibleStatuses = []string{}
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !starts(p, "Lead Back") {
		t.Errorf("benched a questionable 20-point back")
	}
}

// The injury report's comment, like practice participation, shows with the
// player's status, and the same designation from ESPN and the news keeps
// ESPN's projection.
func TestInjuryNoteShowsTheReport(t *testing.T) {
	f, lead, _ := backfield(10.9)
	a := testAgent(f)
	a.Cfg.IREligibleStatuses = []string{}
	a.Injuries = map[int]analysis.Injury{lead.ID: {
		PlayerID: lead.ID, ProTeamID: lead.ProTeamID, Status: espn.InjuryQuestionable, Date: a.Now(),
		Note: "limited practice participant Wednesday",
	}}
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v := p.Values[lead.ID]
	if notes := strings.Join(v.Notes, "; "); !strings.Contains(notes, "QUESTIONABLE: limited practice participant Wednesday") {
		t.Errorf("notes %q", notes)
	}
	if v.WeekProjection != 10.9 || v.PlayChance != 0.85 {
		t.Errorf("projection %.2f, play chance %.2f", v.WeekProjection, v.PlayChance)
	}
}
