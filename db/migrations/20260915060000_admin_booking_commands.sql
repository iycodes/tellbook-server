-- migrate:up
ALTER TABLE booking_change_commands DROP CONSTRAINT booking_change_commands_actor_check;
ALTER TABLE booking_change_commands ADD CONSTRAINT booking_change_commands_actor_check
 CHECK(actor_type IN ('customer','provider','staff'));
-- migrate:down
-- Refuse downgrade if staff command history exists; never delete receipts.
ALTER TABLE booking_change_commands DROP CONSTRAINT booking_change_commands_actor_check;
ALTER TABLE booking_change_commands ADD CONSTRAINT booking_change_commands_actor_check
 CHECK(actor_type IN ('customer','provider'));
