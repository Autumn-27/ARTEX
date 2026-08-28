package db

import (
	"encoding/json"
	"sort"
)

// taskArchiveAggregate is the compact, non-sensitive statistics section of a
// cold archive manifest. Fields are additive so older archive formats remain
// readable when new dashboard dimensions are introduced.
type taskArchiveAggregate struct {
	TokenProfiles     []ProfileUsage    `json:"token_profiles"`
	TokenDaily        []ProfileDayUsage `json:"token_daily"`
	Skills            map[string]int    `json:"skills"`
	SkillStats        []SkillStat       `json:"skill_stats"`
	MissingSkillStats []SkillStat       `json:"missing_skill_stats"`
	Tools             map[string]int    `json:"tools"`
	FindingStats      FindingStats      `json:"finding_stats"`
}

func (d *DB) archivedTaskAggregates() ([]taskArchiveAggregate, error) {
	rawItems, err := d.ArchivedAggregateStats()
	if err != nil {
		return nil, err
	}
	out := make([]taskArchiveAggregate, 0, len(rawItems))
	for _, raw := range rawItems {
		var item taskArchiveAggregate
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func mergeAgentKeys(current, incoming []string) []string {
	seen := make(map[string]bool, len(current)+len(incoming))
	out := make([]string, 0, len(current)+len(incoming))
	for _, group := range [][]string{current, incoming} {
		for _, key := range group {
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func mergeArchivedSkillStats(live []SkillStat, archived []taskArchiveAggregate, missing bool) []SkillStat {
	byName := make(map[string]SkillStat, len(live))
	for _, current := range live {
		byName[current.Skill] = current
	}
	for _, aggregate := range archived {
		coldStats := aggregate.SkillStats
		if missing {
			coldStats = aggregate.MissingSkillStats
		} else if len(coldStats) == 0 {
			// Format v1 initially retained only the call-count map. Preserve those
			// summaries even though agent and timestamp dimensions are unavailable.
			for skill, calls := range aggregate.Skills {
				coldStats = append(coldStats, SkillStat{Skill: skill, Calls: calls, Tasks: 1})
			}
		}
		for _, cold := range coldStats {
			current := byName[cold.Skill]
			current.Skill = cold.Skill
			current.Calls += cold.Calls
			current.Tasks += cold.Tasks
			current.Agents = mergeAgentKeys(current.Agents, cold.Agents)
			if cold.LastUsed != nil && (current.LastUsed == nil || cold.LastUsed.After(*current.LastUsed)) {
				current.LastUsed = cold.LastUsed
			}
			byName[cold.Skill] = current
		}
	}
	out := make([]SkillStat, 0, len(byName))
	for _, current := range byName {
		out = append(out, current)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Skill < out[j].Skill
	})
	return out
}
