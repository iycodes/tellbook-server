-- migrate:up
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_mode_check;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_trigger_check;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_turn_check;
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_mode_check
    CHECK (mode IN ('manual','semi_pilot','autopilot'));
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_trigger_check CHECK (
    (mode='manual' AND trigger_type='provider_on_demand')
    OR (mode IN ('semi_pilot','autopilot') AND trigger_type='customer_message')
);
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_turn_check CHECK (
    jsonb_typeof(structured_decision)='object'
    AND tool_call_count BETWEEN 0 AND 4
    AND ((mode='manual' AND turn_job_id IS NULL AND resulting_message_id IS NULL)
      OR (mode IN ('semi_pilot','autopilot') AND turn_job_id IS NOT NULL))
);

ALTER TABLE inbox_ai_actions DROP CONSTRAINT inbox_ai_actions_name_check;
ALTER TABLE inbox_ai_actions ADD CONSTRAINT inbox_ai_actions_name_check CHECK (action_name IN (
    'list_relevant_services','get_service_details','get_booking_link',
    'search_availability','get_booking_requirements','refresh_booking_proposal',
    'start_booking_session','accept_booking_agreement',
    'confirm_booking_proposal','customer_handoff'
));

-- migrate:down
ALTER TABLE inbox_ai_actions DROP CONSTRAINT inbox_ai_actions_name_check;
ALTER TABLE inbox_ai_actions ADD CONSTRAINT inbox_ai_actions_name_check CHECK (action_name IN (
    'list_relevant_services','get_service_details','get_booking_link',
    'search_availability','get_booking_requirements','refresh_booking_proposal',
    'confirm_booking_proposal','customer_handoff'
));

ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_turn_check;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_trigger_check;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_mode_check;
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_mode_check CHECK (mode IN ('manual','semi_pilot'));
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_trigger_check CHECK (
    (mode='manual' AND trigger_type='provider_on_demand')
    OR (mode='semi_pilot' AND trigger_type='customer_message')
);
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_turn_check CHECK (
    jsonb_typeof(structured_decision)='object'
    AND tool_call_count BETWEEN 0 AND 4
    AND ((mode='manual' AND turn_job_id IS NULL AND resulting_message_id IS NULL)
      OR (mode='semi_pilot' AND turn_job_id IS NOT NULL))
);
