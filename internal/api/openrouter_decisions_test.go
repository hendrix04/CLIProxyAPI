package api

import (
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestTranslateOpenRouterDecisions(t *testing.T) {
	body := []byte(`{"model":"typesafe/jev-1.14","input":"apple","questions":[{"type":"predicate","name":"fruit","instructions":"Is this fruit?"},{"type":"choice","instructions":"Choose.","choices":[{"value":true},{"value":"true"}]},{"type":"score","instructions":"Rate it.","levels":[{"label":"low"},{"label":"high"}]}]}`)
	wire, decode, err := translateDecisions(body)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		State     string                      `json:"state"`
		Questions map[string]decisionQuestion `json:"questions"`
	}
	if err := json.Unmarshal(wire, &request); err != nil || request.State != "apple" || request.Questions["question_0"].Type != "noul" {
		t.Fatalf("unexpected upstream request: %s (%v)", wire, err)
	}
	result, err := decode([]byte(`{"answers":{"question_0":{"type":"noul","noul":0.9},"question_1":{"type":"choice","choice":"option_1","confidence":0.8,"probabilities":{"option_0":0.2,"option_1":0.8}},"question_2":{"type":"score","score":0.7,"confidence":0.7,"probabilities":{"0":0.3,"1":0.7}}},"usage":{"input_tokens":30,"output_tokens":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	var converted struct {
		Answers []map[string]any `json:"answers"`
	}
	if err := json.Unmarshal(result, &converted); err != nil || len(converted.Answers) != 3 {
		t.Fatalf("unexpected OpenAI response: %s (%v)", result, err)
	}
	if converted.Answers[0]["name"] != "fruit" || converted.Answers[0]["type"] != "predicate" || converted.Answers[1]["choice"] != "true" || converted.Answers[2]["score"] != 0.7 {
		t.Fatalf("incorrect Decisions translation: %s", result)
	}
}
