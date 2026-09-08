package tessa

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Coverage describes a bounded live result, not a model's claim about its prose.
// A fulfilled first/top request is complete even if the underlying search has
// additional rows. A list page with additional rows is not complete.
type Coverage struct {
	Tool      string   `json:"tool"`
	EntityIDs []string `json:"entity_ids"`
	Partial   bool     `json:"partial"`
	// Examples may support prose without requiring an exhaustive list when the
	// provider asked for an aggregate or a customer's summary.
	OptionalEntityIDs []string `json:"optional_entity_ids,omitempty"`
}

func composeAnswer(answer *Answer) error {
	if len(answer.Parts) == 0 || len(answer.Parts) > 36 {
		return errors.New("answer needs 1-36 nonempty passages")
	}
	passages := make([]string, 0, len(answer.Parts)+1)
	answer.Mentions = nil
	for _, part := range answer.Parts {
		text := strings.TrimSpace(part.Text)
		if text == "" {
			return errors.New("answer passages must not be empty")
		}
		passages = append(passages, text)
		if part.EntityID != "" {
			answer.Mentions = append(answer.Mentions, AnswerMention{EntityID: part.EntityID, Quote: text})
		}
	}
	answer.Content = strings.Join(passages, "\n\n")
	answer.PartialNotice = strings.TrimSpace(answer.PartialNotice)
	if answer.PartialNotice != "" {
		answer.Content += "\n\n" + answer.PartialNotice
	}
	return nil
}

func ResultCoverage(input SynthesisInput) ([]Coverage, error) {
	result := make([]Coverage, 0, len(input.Evidence))
	for index, evidence := range input.Evidence {
		var body struct {
			Items    []map[string]json.RawMessage `json:"items"`
			HasMore  bool                         `json:"has_more"`
			Found    bool                         `json:"found"`
			Booking  map[string]json.RawMessage   `json:"booking"`
			Service  map[string]json.RawMessage   `json:"service"`
			Payment  map[string]json.RawMessage   `json:"payment"`
			Services []map[string]json.RawMessage `json:"services"`
			Examples []map[string]json.RawMessage `json:"examples"`
			Customer struct {
				Customer       map[string]json.RawMessage   `json:"customer"`
				RecentBookings []map[string]json.RawMessage `json:"recent_bookings"`
			} `json:"customer"`
		}
		if err := json.Unmarshal(evidence.Result, &body); err != nil {
			return nil, errors.New("invalid safe evidence")
		}
		coverage := Coverage{Tool: evidence.Tool, Partial: body.HasMore}
		key := ""
		switch evidence.Tool {
		case "search_bookings", "get_schedule":
			key = "booking_id"
		case "search_services":
			key = "service_id"
		case "search_customers":
			key = "customer_id"
		case "get_booking":
			key = "booking_id"
			if body.Found {
				body.Items = []map[string]json.RawMessage{body.Booking}
			}
		case "get_service":
			key = "service_id"
			if body.Found {
				body.Items = []map[string]json.RawMessage{body.Service}
			}
		case "get_booking_payment_status":
			key = "booking_id"
			if body.Found {
				body.Items = []map[string]json.RawMessage{body.Payment}
			}
		case "get_customer_booking_summary":
			key = "customer_id"
			if body.Found {
				body.Items = []map[string]json.RawMessage{body.Customer.Customer}
				body.Examples = body.Customer.RecentBookings
			}
		case "get_booking_attention_summary":
			// The counts are complete; examples are not the requested total.
		case "get_availability":
			key, body.Items = "service_id", body.Services
		default:
			continue
		}
		seen := map[string]bool{}
		for _, item := range body.Examples {
			var id string
			if err := json.Unmarshal(item["booking_id"], &id); err != nil || !validUUID(id) {
				return nil, errors.New("safe evidence has an invalid example ID")
			}
			if !seen[id] {
				coverage.OptionalEntityIDs = append(coverage.OptionalEntityIDs, id)
				seen[id] = true
			}
		}
		for _, item := range body.Items {
			var id string
			if err := json.Unmarshal(item[key], &id); err != nil || !validUUID(id) {
				return nil, errors.New("safe evidence has an invalid entity ID")
			}
			if !seen[id] {
				coverage.EntityIDs = append(coverage.EntityIDs, id)
				seen[id] = true
			}
		}
		if index < len(input.Tools) {
			request := input.Tools[index]
			if request.Name != evidence.Tool {
				return nil, errors.New("tool evidence order does not match plan")
			}
			if request.Selection == "first" || request.Selection == "top" {
				coverage.Partial = body.HasMore && len(coverage.EntityIDs) < request.Limit
			}
		}
		result = append(result, coverage)
	}
	return result, nil
}

var answerLinkOrID = regexp.MustCompile(`(?i)https?://|www\.|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func validateAnswer(answer Answer, input SynthesisInput) error {
	if !utf8.ValidString(answer.Content) || answer.Content == "" || utf8.RuneCountInString(answer.Content) > input.MaxAnswerCharacters {
		return errors.New("answer length or encoding is invalid")
	}
	if answerLinkOrID.MatchString(answer.Content) {
		return errors.New("omit URLs and raw entity IDs; navigation is attached separately")
	}
	coverage, err := ResultCoverage(input)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	required := map[string]bool{}
	partial := false
	for _, collection := range coverage {
		partial = partial || collection.Partial
		for _, id := range collection.EntityIDs {
			allowed[id] = true
			required[id] = true
		}
		for _, id := range collection.OptionalEntityIDs {
			allowed[id] = true
		}
	}
	if len(answer.Mentions) > MaxToolCalls*MaxSearchResults {
		return errors.New("too many entity mentions")
	}
	mentioned := map[string]bool{}
	for _, mention := range answer.Mentions {
		quote := strings.TrimSpace(mention.Quote)
		if !allowed[mention.EntityID] {
			return errors.New("each entity_id must identify an entity from the current evidence")
		}
		if quote == "" || !strings.Contains(answer.Content, quote) {
			return errors.New("each mention needs its own exact passage from the visible answer")
		}
		mentioned[mention.EntityID] = true
	}
	if len(required) > 0 && len(mentioned) == 0 {
		return errors.New("at least one parts entry must set entity_id to an ID from coverage.entity_ids; booking/service/customer descriptions must not use an empty entity_id")
	}
	for id := range required {
		if !mentioned[id] {
			partial = true
		}
	}
	notice := strings.TrimSpace(answer.PartialNotice)
	if partial && notice == "" {
		return errors.New("results are partial: evidence or answer omits matching items; write a natural explanation in partial_notice")
	}
	if !partial && notice != "" {
		return errors.New("requested selection is complete: partial_notice must be empty, regardless of underlying has_more")
	}
	if notice != "" && !strings.Contains(answer.Content, notice) {
		return errors.New("partial_notice must be an exact visible passage explaining that this is not the full list")
	}
	return nil
}
