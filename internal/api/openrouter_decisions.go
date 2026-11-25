package api

import (
	"fmt"
	"strconv"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
)

type decisionDecoder func([]byte) ([]byte, error)
type decisionTranslator func([]byte) ([]byte, decisionDecoder, error)

type decisionQuestion struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Choices      []struct {
		Value       any    `json:"value"`
		Description string `json:"description"`
	} `json:"choices"`
	Levels []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"levels"`
}

type routerDecisionAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Reason        string             `json:"reason"`
}

// Translate the OpenAI SDK's text-input Decisions shape. OpenRouter-native
// callers can send state and get the native envelope unchanged.
func translateDecisions(body []byte) ([]byte, decisionDecoder, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, nil, err
	}
	if _, native := fields["state"]; native {
		if _, mixed := fields["input"]; mixed {
			return nil, nil, fmt.Errorf("send input or state, not both")
		}
		return body, nil, nil
	}
	for field := range fields {
		switch field {
		case "model", "input", "questions":
		default:
			return nil, nil, fmt.Errorf("unsupported Decisions field: %s", field)
		}
	}
	var input struct {
		Model     string             `json:"model"`
		Input     string             `json:"input"`
		Questions []decisionQuestion `json:"questions"`
	}
	if err := json.Unmarshal(body, &input); err != nil || input.Input == "" || len(input.Questions) == 0 {
		return nil, nil, fmt.Errorf("OpenAI-compatible Decisions requires text input and a non-empty questions array")
	}
	questions := make(map[string]any, len(input.Questions))
	for i, question := range input.Questions {
		key := "question_" + strconv.Itoa(i)
		translated := map[string]any{"type": question.Type, "instructions": question.Instructions}
		if question.Instructions == "" {
			return nil, nil, fmt.Errorf("question %d requires text instructions", i)
		}
		switch question.Type {
		case "predicate":
			translated["type"] = "noul"
		case "choice":
			criteria := make(map[string]string, len(question.Choices))
			seen := make(map[any]bool, len(question.Choices))
			for j, choice := range question.Choices {
				var label string
				switch value := choice.Value.(type) {
				case string:
					label = value
				case bool:
					label = strconv.FormatBool(value)
				default:
					return nil, nil, fmt.Errorf("question %d has a choice that is neither a string nor a boolean", i)
				}
				if seen[choice.Value] {
					return nil, nil, fmt.Errorf("question %d has duplicate choices", i)
				}
				seen[choice.Value] = true
				criteria["option_"+strconv.Itoa(j)] = label + ": " + choice.Description
			}
			translated["criteria"] = criteria
		case "score":
			criteria := make([]string, 0, len(question.Levels))
			for _, level := range question.Levels {
				criteria = append(criteria, level.Label+": "+level.Description)
			}
			translated["criteria"] = criteria
		default:
			return nil, nil, fmt.Errorf("question %d has an unsupported type", i)
		}
		questions[key] = translated
	}
	doc := ojson.NewDocString("{}")
	for key, value := range map[string]any{"model": input.Model, "state": input.Input, "questions": questions} {
		if err := doc.Set(key, value); err != nil {
			return nil, nil, err
		}
	}
	return doc.Bytes(), func(response []byte) ([]byte, error) {
		return decodeDecisions(response, input.Model, input.Questions)
	}, nil
}

func decodeDecisions(body []byte, model string, questions []decisionQuestion) ([]byte, error) {
	var response struct {
		Answers map[string]routerDecisionAnswer `json:"answers"`
		Usage   json.RawMessage                 `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	answers := make([]map[string]any, 0, len(questions))
	for i, question := range questions {
		result, found := response.Answers["question_"+strconv.Itoa(i)]
		if !found || (result.Type != question.Type && !(result.Type == "noul" && question.Type == "predicate") && result.Type != "refusal") {
			return nil, fmt.Errorf("provider did not return a matching answer for question %d", i)
		}
		answer := map[string]any{"type": question.Type}
		if question.Name != "" {
			answer["name"] = question.Name
		}
		switch result.Type {
		case "noul":
			if result.Noul == nil {
				return nil, fmt.Errorf("predicate has no probability")
			}
			answer["probability"] = *result.Noul
		case "choice":
			options := make([]map[string]any, 0, len(question.Choices))
			for j, option := range question.Choices {
				key := "option_" + strconv.Itoa(j)
				p, ok := result.Probabilities[key]
				if !ok {
					return nil, fmt.Errorf("choice lacks option probabilities")
				}
				options = append(options, map[string]any{"value": option.Value, "probability": p})
				if result.Choice == key {
					answer["choice"] = option.Value
				}
			}
			if _, ok := answer["choice"]; !ok || result.Confidence == nil {
				return nil, fmt.Errorf("choice has no valid winner or confidence")
			}
			answer["probabilities"], answer["confidence"] = options, *result.Confidence
		case "score":
			levels := make([]map[string]any, 0, len(question.Levels))
			for j, level := range question.Levels {
				p, ok := result.Probabilities[strconv.Itoa(j)]
				if !ok {
					return nil, fmt.Errorf("score lacks level probabilities")
				}
				levels = append(levels, map[string]any{"value": j, "label": level.Label, "probability": p})
			}
			if result.Score == nil || result.Confidence == nil {
				return nil, fmt.Errorf("score has no value or confidence")
			}
			answer["probabilities"], answer["score"], answer["confidence"] = levels, *result.Score, *result.Confidence
		case "refusal":
			answer["type"], answer["reason"] = "refusal", result.Reason
		default:
			return nil, fmt.Errorf("unsupported answer type")
		}
		answers = append(answers, answer)
	}
	doc := ojson.NewDocString("{}")
	if err := doc.Set("model", model); err != nil {
		return nil, err
	}
	if err := doc.Set("answers", answers); err != nil {
		return nil, err
	}
	if len(response.Usage) > 0 {
		if err := doc.SetRaw("usage", string(response.Usage)); err != nil {
			return nil, err
		}
	}
	return doc.Bytes(), nil
}
