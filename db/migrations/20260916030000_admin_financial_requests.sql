-- migrate:up
-- Approval metadata only. Workers must never claim these rows as money jobs.
CREATE TABLE admin_financial_requests (
 id uuid PRIMARY KEY,
 kind text NOT NULL CHECK(kind='payout'),
 requester_id uuid NOT NULL REFERENCES admin_staff(id),
 request_key uuid NOT NULL,
 business_id uuid NOT NULL REFERENCES clients(id),
 allocation_id uuid NOT NULL REFERENCES payment_allocations(id),
 destination_id uuid NOT NULL REFERENCES payout_destinations(id),
 terms jsonb NOT NULL CHECK(jsonb_typeof(terms)='object'),
 terms_fingerprint text NOT NULL CHECK(terms_fingerprint ~ '^[a-f0-9]{64}$'),
 reason text NOT NULL CHECK(char_length(reason) BETWEEN 1 AND 1000),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected','withdrawn')),
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 reviewer_id uuid REFERENCES admin_staff(id),
 decision_reason text NOT NULL DEFAULT '',
 decision_key uuid,
 decided_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(requester_id,request_key),
 CHECK((status='pending' AND reviewer_id IS NULL AND decided_at IS NULL AND decision_key IS NULL AND decision_reason='') OR
       (status<>'pending' AND reviewer_id IS NOT NULL AND decided_at IS NOT NULL AND decision_key IS NOT NULL AND char_length(decision_reason) BETWEEN 1 AND 1000)),
 CHECK(status NOT IN ('approved','rejected') OR reviewer_id<>requester_id)
);
CREATE UNIQUE INDEX admin_financial_open_allocation_idx ON admin_financial_requests(allocation_id) WHERE status IN ('pending','approved');
CREATE INDEX admin_financial_request_queue_idx ON admin_financial_requests(created_at DESC,id DESC);
-- migrate:down
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM admin_financial_requests) THEN
  RAISE EXCEPTION 'Retain financial approval history; disable application controls instead';
 END IF;
END $$;
DROP TABLE admin_financial_requests;
