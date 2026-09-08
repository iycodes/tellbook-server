package appdata

import (
	"context"
	"time"

	"booking/go-server/internal/whatsapp"
)

func (worker *TessaWorker) WithWhatsAppTyping(sender whatsapp.TypingSender) *TessaWorker {
	worker.whatsAppTyping = sender
	return worker
}

// Called only inside the capacity-bounded, active turn lifecycle. Cosmetic
// presence has no queue, repeated timer or retry/restart replay.
func (worker *TessaWorker) startWhatsAppTyping(ctx context.Context, run tessaClaimedRun) func() {
	if worker.whatsAppTyping == nil || run.SourceChannel != "whatsapp" || run.AttemptCount != 1 || ctx.Err() != nil {
		return func() {}
	}
	typingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var source string
		if err := worker.repo.db.QueryRow(typingCtx, `SELECT source_message_id FROM tessa_whatsapp_ingress
          WHERE run_id=$1 AND status='admitted' AND phone_number_id=$2 AND source_timestamp+INTERVAL '24 hours'>NOW()`, run.ID, worker.config.WhatsAppPhoneNumberID).Scan(&source); err != nil {
			return
		}
		if typingCtx.Err() != nil || worker.checkWhatsAppAuthority(typingCtx, worker.repo.db, run) != nil {
			return
		}
		// Failure is deliberately ignored: it must not fail generation or delivery.
		_ = worker.whatsAppTyping.SendTyping(typingCtx, source)
	}()
	return func() { cancel(); <-done }
}
