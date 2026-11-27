package model

// Headline is a finished turn's one line, written by the agent's cheapest
// model: past tense, as "Started SFT on the H100; 3,478 steps planned".
// Turn is the turn's id in the chat's snapshot (session.TurnSummary.ID).
type Headline struct {
	Turn string `json:"turn"`
	Text string `json:"text"`
}

// HeadlineOf is the headline of turn in hs, "" for none.
func HeadlineOf(hs []Headline, turn string) string {
	for i := len(hs) - 1; i >= 0; i-- {
		if hs[i].Turn == turn {
			return hs[i].Text
		}
	}
	return ""
}
