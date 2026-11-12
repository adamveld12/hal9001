package outschema

import (
	"fmt"
	"strings"
	"testing"
)

func questionJSON(id, header string, opts int, extra string) string {
	var os []string
	for i := 0; i < opts; i++ {
		os = append(os, fmt.Sprintf(`{"label":"opt%d"}`, i))
	}
	h := ""
	if header != "" {
		h = fmt.Sprintf(`"header":%q,`, header)
	}
	return fmt.Sprintf(`{"id":%q,%s"question":"Which?","options":[%s]%s}`, id, h, strings.Join(os, ","), extra)
}

func noteWith(questions []string, actions []string) []byte {
	return []byte(fmt.Sprintf(`{"title":"T","summary_md":"s","questions":[%s],"actions":[%s]}`, strings.Join(questions, ","), strings.Join(actions, ",")))
}

// Every limit is enforced by the note schema or by ParseInteractive: a call that breaks one is refused by one of them.
func TestInteractiveLimits(t *testing.T) {
	q := func(id string) string { return questionJSON(id, "", 2, "") }
	act := `{"label":"Go","prompt":"Do it."}`
	many := func(n int, s string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	five := []string{q("a"), q("b"), q("c"), q("d"), q("e")}
	for name, tc := range map[string]struct {
		note []byte
		ok   bool
	}{
		"empty":                    {noteWith(nil, nil), true},
		"four questions":           {noteWith(five[:4], nil), true},
		"five questions":           {noteWith(five, nil), false},
		"five actions":             {noteWith(nil, many(5, act)), true},
		"six actions":              {noteWith(nil, many(6, act)), false},
		"two options":              {noteWith([]string{questionJSON("a", "", 2, "")}, nil), true},
		"five options":             {noteWith([]string{questionJSON("a", "", 5, "")}, nil), true},
		"one option":               {noteWith([]string{questionJSON("a", "", 1, "")}, nil), false},
		"six options":              {noteWith([]string{questionJSON("a", "", 6, "")}, nil), false},
		"header 12":                {noteWith([]string{questionJSON("a", "123456789012", 2, "")}, nil), true},
		"header 13":                {noteWith([]string{questionJSON("a", "1234567890123", 2, "")}, nil), false},
		"duplicate ids":            {noteWith([]string{q("a"), q("a")}, nil), false},
		"id with capitals":         {noteWith([]string{q("Strategy")}, nil), false},
		"id with space":            {noteWith([]string{q("a b")}, nil), false},
		"id starting with dash":    {noteWith([]string{q("-a")}, nil), false},
		"recommended in range":     {noteWith([]string{questionJSON("a", "", 3, `,"recommended":2`)}, nil), true},
		"recommended out of range": {noteWith([]string{questionJSON("a", "", 2, `,"recommended":2`)}, nil), false},
		"recommended negative":     {noteWith([]string{questionJSON("a", "", 2, `,"recommended":-1`)}, nil), false},
		"multi":                    {noteWith([]string{questionJSON("a", "", 2, `,"multi":true`)}, nil), true},
		"repeated option label":    {noteWith([]string{`{"id":"a","question":"Q?","options":[{"label":"x"},{"label":"x"}]}`}, nil), false},
		"extra question property":  {noteWith([]string{questionJSON("a", "", 2, `,"color":"red"`)}, nil), false},
		"action label 40":          {noteWith(nil, []string{`{"label":"` + strings.Repeat("l", 40) + `","prompt":"p"}`}), true},
		"action label 41":          {noteWith(nil, []string{`{"label":"` + strings.Repeat("l", 41) + `","prompt":"p"}`}), false},
		"empty prompt":             {noteWith(nil, []string{`{"label":"Go","prompt":""}`}), false},
		"blank prompt":             {noteWith(nil, []string{`{"label":"Go","prompt":"  "}`}), false},
		"prompt 2000":              {noteWith(nil, []string{`{"label":"Go","prompt":"` + strings.Repeat("p", 2000) + `"}`}), true},
		"prompt 2001":              {noteWith(nil, []string{`{"label":"Go","prompt":"` + strings.Repeat("p", 2001) + `"}`}), false},
		"explicit prompt type":     {noteWith(nil, []string{`{"type":"prompt","label":"Go","prompt":"p"}`}), true},
		"unknown action type":      {noteWith(nil, []string{`{"type":"shell","label":"Go","prompt":"p"}`}), false},
		"extra action property":    {noteWith(nil, []string{`{"label":"Go","prompt":"p","url":"x"}`}), false},
	} {
		t.Run(name, func(t *testing.T) {
			err := Note().Validate(tc.note)
			if err == nil {
				_, _, err = ParseInteractive(tc.note)
			}
			if tc.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// Check alone also refuses what the schema would (the text-parse path never sees the schema).
func TestCheckWithoutSchema(t *testing.T) {
	two := []Option{{Label: "a"}, {Label: "b"}}
	bad := 5
	for name, qs := range map[string][]Question{
		"one option":      {{ID: "a", Question: "Q?", Options: two[:1]}},
		"long header":     {{ID: "a", Question: "Q?", Header: strings.Repeat("h", 13), Options: two}},
		"duplicate ids":   {{ID: "a", Question: "Q?", Options: two}, {ID: "a", Question: "Q?", Options: two}},
		"bad recommended": {{ID: "a", Question: "Q?", Options: two, Recommended: &bad}},
	} {
		if err := Check(qs, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := Check(nil, []Action{{Type: "shell", Label: "l", Prompt: "p"}}); err == nil {
		t.Error("unknown action type accepted")
	}
	_, as, err := ParseInteractive([]byte(`{"actions":[{"label":"l","prompt":"p"}]}`))
	if err != nil || len(as) != 1 || as[0].Type != ActionPrompt {
		t.Errorf("default action type: %+v %v", as, err)
	}
}
