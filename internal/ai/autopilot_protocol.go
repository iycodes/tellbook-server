package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Autopilot deliberately uses the same small decision envelope as semi-pilot,
// but has its own state and tool allowlists. Keeping the parser separate means
// a future schema change cannot accidentally grant semi-pilot an autopilot tool.
func ParseAutopilotTurnDecision(payload []byte) (SemiPilotTurnDecision, error) {
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
	if !validAutopilotConversationState(decision.NextState) {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: next_state is invalid", ErrInvalidSemiPilotDecision)
	}
	if utf8.RuneCountInString(decision.Reply) > semiPilotReplyMaxRunes || containsModelGeneratedLink(decision.Reply) {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: reply is invalid", ErrInvalidSemiPilotDecision)
	}
	if len(decision.MissingFacts) > 8 {
		return SemiPilotTurnDecision{}, fmt.Errorf("%w: too many missing facts", ErrInvalidSemiPilotDecision)
	}
	for index := range decision.MissingFacts {
		decision.MissingFacts[index] = strings.TrimSpace(decision.MissingFacts[index])
		if decision.MissingFacts[index] == "" || utf8.RuneCountInString(decision.MissingFacts[index]) > semiPilotMissingFactMaxRunes {
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
	if err := validateAutopilotToolCall(decision.ToolCall); err != nil {
		return SemiPilotTurnDecision{}, err
	}
	return decision, nil
}

func validateAutopilotToolCall(call *SemiPilotToolCall) error {
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
		if utf8.RuneCountInString(strings.TrimSpace(arguments.Query)) > 160 {
			return fmt.Errorf("%w: service query is too long", ErrInvalidSemiPilotDecision)
		}
	case "get_service_details":
		var arguments SemiPilotServiceArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil || !looksLikeUUID(arguments.ServiceID) {
			return fmt.Errorf("%w: get_service_details arguments are invalid", ErrInvalidSemiPilotDecision)
		}
	default:
		return fmt.Errorf("%w: unknown autopilot tool", ErrInvalidSemiPilotDecision)
	}
	return nil
}

func validAutopilotConversationState(state string) bool {
	switch state {
	case "qualifying", "service_identified", "collecting_preferences",
		"collecting_customer_details", "offering_slots", "preparing_proposal",
		"awaiting_before_booking_agreement", "awaiting_confirmation", "creating_reservation",
		"reservation_created", "awaiting_payment", "awaiting_after_payment_agreement",
		"awaiting_provider_confirmation", "handoff":
		return true
	default:
		return false
	}
}

func AutopilotDecisionJSONSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["protocol_version","reply","next_state","tool_call","missing_facts","handoff_reason"],
  "properties":{
    "protocol_version":{"type":"integer","const":1},
    "reply":{"type":"string"},
    "next_state":{"type":"string","enum":["qualifying","service_identified","collecting_preferences","collecting_customer_details","offering_slots","preparing_proposal","awaiting_before_booking_agreement","awaiting_confirmation","creating_reservation","reservation_created","awaiting_payment","awaiting_after_payment_agreement","awaiting_provider_confirmation","handoff"]},
    "tool_call":{
      "type":["object","null"],"additionalProperties":false,"required":["name","arguments"],
      "properties":{
        "name":{"type":"string","enum":["list_relevant_services","get_service_details"]},
        "arguments":{
          "anyOf":[
            {"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}},
            {"type":"object","additionalProperties":false,"required":["service_id"],"properties":{"service_id":{"type":"string"}}}
          ]
        }
      }
    },
    "missing_facts":{"type":"array","items":{"type":"string"}},
    "handoff_reason":{"type":"string","enum":["","customer_requested_provider","unsupported_request","sensitive_request","service_not_found","model_or_tool_failure","identity_uncertain"]}
  }
}`)
}
