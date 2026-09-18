-- migrate:up
CREATE TABLE admin_support_cases (
 id uuid PRIMARY KEY,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 160),
 description text NOT NULL CHECK(char_length(description) BETWEEN 1 AND 4000),
 status text NOT NULL CHECK(status IN ('open','waiting','resolved')),
 priority text NOT NULL CHECK(priority IN ('low','normal','high','urgent')),
 assignee_id uuid REFERENCES admin_staff(id),
 follow_up date,
 resolution text NOT NULL DEFAULT '',
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 business_id uuid REFERENCES client_profiles(client_id),
 booking_id uuid REFERENCES bookings(id),
 contact_id uuid REFERENCES customers(id),
 account_id uuid REFERENCES marketplace_customers(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(num_nonnulls(business_id,booking_id,contact_id,account_id)<=1),
 CHECK((status='resolved')=(resolution<>''))
);
CREATE INDEX admin_cases_queue_idx ON admin_support_cases(created_at DESC,id DESC);
CREATE INDEX admin_cases_due_idx ON admin_support_cases(follow_up) WHERE status<>'resolved';
CREATE INDEX admin_cases_assignee_idx ON admin_support_cases(assignee_id,status);
CREATE TABLE admin_support_changes (
 actor_id uuid NOT NULL REFERENCES admin_staff(id),
 request_key uuid NOT NULL,
 case_id uuid NOT NULL REFERENCES admin_support_cases(id),
 fingerprint bytea NOT NULL,
 revision integer NOT NULL,
 action text NOT NULL CHECK(action IN ('created','updated','resolved','reopened')),
 reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,request_key),
 UNIQUE(case_id,revision)
);
ALTER TABLE admin_business_notes ADD COLUMN case_id uuid REFERENCES admin_support_cases(id),
 DROP CONSTRAINT admin_notes_one_target,
 ADD CONSTRAINT admin_notes_one_target CHECK(num_nonnulls(business_id,booking_id,contact_id,account_id,case_id)=1);
CREATE INDEX admin_notes_case_idx ON admin_business_notes(case_id,created_at DESC,id DESC) WHERE case_id IS NOT NULL;
-- migrate:down
-- Never silently remove staff case history during rollback.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM admin_support_cases) THEN
  RAISE EXCEPTION 'Retain support cases and history; roll back application controls instead';
 END IF;
END $$;
ALTER TABLE admin_business_notes DROP CONSTRAINT admin_notes_one_target, DROP COLUMN case_id,
 ADD CONSTRAINT admin_notes_one_target CHECK(num_nonnulls(business_id,booking_id,contact_id,account_id)=1);
DROP TABLE admin_support_changes;
DROP TABLE admin_support_cases;
