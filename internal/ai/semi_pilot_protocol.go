package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	SemiPilotProtocolVersion     = 1
	SemiPilotMaxToolCallsPerTurn = 4
	semiPilotDecisionMaxBytes    = 16 * 1024
	semiPilotReplyMaxRunes       = 2000
	semiPilotMissingFactMaxRunes = 80
)

var ErrInvalidSemiPilotDecision = errors.New("invalid semi-pilot decision")

type SemiPilotTurnDecision struct {
	ProtocolVersion int                `json:"protocol_version"`
	Reply           string             `json:"reply"`
	NextState       string             `json:"next_state"`
	ToolCall        *SemiPilotToolCall `json:"tool_call"`
	MissingFacts    []string           `json:"missing_facts"`
	HandoffReason   string             `json:"handoff_reason"`
}

type SemiPilotToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type SemiPilotListRelevantServicesArguments struct {
	Query string `json:"query"`
}

type SemiPilotServiceArguments struct {
	ServiceID string `json:"service_id"`
}

type SemiPilotBookingLinkArguments struct {
	ServiceID string `json:"service_id"`
}

func ParseSemiPilotTurnDecision(payload []byte) (SemiPilotTurnDecision, error) {
	if len(payload) == 0 || len(payload) > semiPilotDecisionMaxBytes {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: decision size is invalid", ErrInvalidSemiPilotDecision)
	}
	if err := requireSemiPilotDecisionProperties(payload); err != nil {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: %v", ErrInvalidSemiPilotDecision, err)
	}
	var decision SemiPilotTurnDecision
	if err := decodeStrictJSON(payload, &decision); err != nil {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: %v", ErrInvalidSemiPilotDecision, err)
	}
	decision.Reply = strings.TrimSpace(decision.Reply)
	decision.NextState = strings.TrimSpace(decision.NextState)
	decision.HandoffReason = strings.TrimSpace(decision.HandoffReason)
	if decision.ProtocolVersion != SemiPilotProtocolVersion {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: unsupported protocol version", ErrInvalidSemiPilotDecision)
	}
	if !validSemiPilotState(decision.NextState) {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: next_state is invalid", ErrInvalidSemiPilotDecision)
	}
	if utf8.RuneCountInString(decision.Reply) > semiPilotReplyMaxRunes {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: reply is too long", ErrInvalidSemiPilotDecision)
	}
	if containsModelGeneratedLink(decision.Reply) {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: reply must not contain a generated link", ErrInvalidSemiPilotDecision)
	}
	if len(decision.MissingFacts) > 8 {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: too many missing facts", ErrInvalidSemiPilotDecision)
	}
	for index := range decision.MissingFacts {
		decision.MissingFacts[index] = strings.TrimSpace(decision.MissingFacts[index])
		if decision.MissingFacts[index] == "" ||
			utf8.RuneCountInString(decision.MissingFacts[index]) > semiPilotMissingFactMaxRunes {
			return SemiPilotTurnDecision{}, fmt.Errorf("%w: missing fact is invalid", ErrInvalidSemiPilotDecision)
		}
	}
	if decision.NextState == "handoff" {
		if !validSemiPilotHandoffReason(decision.HandoffReason) || decision.ToolCall != nil {
			return SemiPilotTurnDecision{}, fmt.Errorf("%w: handoff decision is invalid", ErrInvalidSemiPilotDecision)
		}
	} else if decision.HandoffReason != "" {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: handoff_reason is only valid for handoff", ErrInvalidSemiPilotDecision)
	}
	if decision.ToolCall == nil {
		if decision.Reply == "" {
			return SemiPilotTurnDecision{}, fmt.Errorf("%w: reply is required without a tool call", ErrInvalidSemiPilotDecision)
		}
		return decision, nil
	}
	if err := validateSemiPilotToolCall(decision.ToolCall); err != nil {
		return SemiPilotTurnDecision{}, err
	}
	return decision, nil
}

func requireSemiPilotDecisionProperties(payload []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	required := []string{
		"protocol_version", "reply", "next_state", "tool_call", "missing_facts", "handoff_reason",
	}
	for _, property := range required {
		value, exists := envelope[property]
		if !exists {
			return fmt.Errorf("required property %q is missing", property)
		}
		if property == "missing_facts" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("missing_facts must be an array")
		}
	}
	return nil
}

func validateSemiPilotToolCall(call *SemiPilotToolCall) error {
	call.Name = strings.TrimSpace(call.Name)
	argumentsJSON := bytes.TrimSpace(call.Arguments)
	if len(argumentsJSON) < 2 || argumentsJSON[0] != '{' || argumentsJSON[len(argumentsJSON)-1] != '}' {
		return fmt.Errorf("%w: tool arguments must be an object", ErrInvalidSemiPilotDecision)
	}
	switch call.Name {
	case "list_relevant_services":
		var arguments SemiPilotListRelevantServicesArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return fmt.Errorf("%w: list_relevant_services arguments: %v", ErrInvalidSemiPilotDecision, err)
		}
		arguments.Query = strings.TrimSpace(arguments.Query)
		if utf8.RuneCountInString(arguments.Query) > 160 {
			return fmt.Errorf("%w: service query is too long", ErrInvalidSemiPilotDecision)
		}
	case "get_service_details":
		var arguments SemiPilotServiceArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return fmt.Errorf("%w: get_service_details arguments: %v", ErrInvalidSemiPilotDecision, err)
		}
		if !looksLikeUUID(arguments.ServiceID) {
			return fmt.Errorf("%w: service_id is invalid", ErrInvalidSemiPilotDecision)
		}
	case "get_booking_link":
		var arguments SemiPilotBookingLinkArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return fmt.Errorf("%w: get_booking_link arguments: %v", ErrInvalidSemiPilotDecision, err)
		}
		if strings.TrimSpace(arguments.ServiceID) != "" && !looksLikeUUID(arguments.ServiceID) {
			return fmt.Errorf("%w: service_id is invalid", ErrInvalidSemiPilotDecision)
		}
	default:
		return fmt.Errorf("%w: unknown semi-pilot tool", ErrInvalidSemiPilotDecision)
	}
	return nil
}

func decodeStrictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func validSemiPilotState(state string) bool {
	switch state {
	case "qualifying", "service_identified", "link_ready", "link_sent", "completed", "handoff":
		return true
	default:
		return false
	}
}

func validSemiPilotHandoffReason(reason string) bool {
	switch reason {
	case "customer_requested_provider", "unsupported_request", "sensitive_request",
		"service_not_found", "model_or_tool_failure", "identity_uncertain":
		return true
	default:
		return false
	}
}

func containsModelGeneratedLink(reply string) bool {
	normalized := strings.ToLower(reply)
	for _, marker := range []string{"http://", "https://", "www.", "/booking", "/providers/"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func looksLikeUUID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func SemiPilotDecisionJSONSchema() json.RawMessage {
	// Keep size/length limits in ParseSemiPilotTurnDecision. The configured
	// llama-server build rejects otherwise valid generations when JSON Schema
	// maxLength/maxItems keywords are present, while the application validator
	// enforces the same bounds deterministically after decoding.
	return json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["protocol_version","reply","next_state","tool_call","missing_facts","handoff_reason"],
  "properties":{
    "protocol_version":{"type":"integer","const":1},
    "reply":{"type":"string"},
    "next_state":{"type":"string","enum":["qualifying","service_identified","link_ready","link_sent","completed","handoff"]},
    "tool_call":{
      "type":["object","null"],"additionalProperties":false,"required":["name","arguments"],
      "properties":{
        "name":{"type":"string","enum":["list_relevant_services","get_service_details","get_booking_link"]},
        "arguments":{
          "anyOf":[
            {"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}},
            {"type":"object","additionalProperties":false,"required":["service_id"],"properties":{"service_id":{"type":"string"}}}
          ]
        }
      }
    },
    "missing_facts":{"type":"array","items":{"type":"string"}},
    "handoff_reason":{"type":"string"}
  }
}`)
}
