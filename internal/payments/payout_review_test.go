package payments

import (
	"booking/go-server/internal/secure"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestReviewedPayoutRecipientSnapshot(t *testing.T) {
	keyring, err := secure.ParseKeyring(fmt.Sprintf(`{"v1":%q}`, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))), "v1")
	if err != nil {
		t.Fatal(err)
	}
	d := PayoutDestination{ID: uuid.New(), ClientID: uuid.New(), Provider: "paystack", CountryCode: "NG", CurrencyCode: "NGN", Rail: "bank_account", InstitutionCode: "001", InstitutionName: "Original bank", ResolvedAccountName: "Original owner", ProviderRecipientID: "original-recipient"}
	ciphertext, err := keyring.Encrypt([]byte("0123456789"), payoutDestinationAAD(d.ClientID, d.ID))
	if err != nil {
		t.Fatal(err)
	}
	d.IdentifierCiphertext, d.IdentifierNonce, d.EncryptionKeyVersion = ciphertext.Data, ciphertext.Nonce, ciphertext.KeyVersion
	payout := FinancialPayout{ClientID: d.ClientID, PayoutDestinationID: d.ID, Provider: d.Provider, CountryCode: d.CountryCode, CurrencyCode: d.CurrencyCode, Rail: d.Rail}
	snapshot := func(destination PayoutDestination) []byte {
		raw, e := json.Marshal(map[string]any{"reviewed_fingerprint": strings.Repeat("a", 64), "reviewed_destination": destination})
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	payout.DestinationSnapshot = snapshot(d)
	// No repository: the reviewed path must not read a live destination at all.
	service := &PayoutService{ledger: &LedgerService{keyring: keyring}}
	d.ProviderRecipientID = "changed-recipient"
	d.ResolvedAccountName = "Changed owner"
	recipient, err := service.recipientForPayout(context.Background(), payout, false)
	if err != nil || recipient.ProviderReference != "original-recipient" || recipient.AccountName != "Original owner" || recipient.Identifier != "" {
		t.Fatal("dispatch snapshot", recipient, err)
	}
	recipient, err = service.recipientForPayout(context.Background(), payout, true)
	if err != nil || recipient.ProviderReference != "original-recipient" || recipient.Identifier != "0123456789" {
		t.Fatal("reconciliation snapshot", recipient, err)
	}
	d.ProviderRecipientID = ""
	payout.DestinationSnapshot = snapshot(d)
	recipient, err = service.recipientForPayout(context.Background(), payout, false)
	if err != nil || recipient.Identifier != "0123456789" {
		t.Fatal("account identifier snapshot", recipient, err)
	}
	d.CurrencyCode = "USD"
	payout.DestinationSnapshot = snapshot(d)
	if _, err = service.recipientForPayout(context.Background(), payout, false); err == nil {
		t.Fatal("mismatched snapshot accepted")
	}
	for _, raw := range []string{`{"reviewed_fingerprint":null}`, `{"reviewed_destination":null}`, `{"reviewed_fingerprint":"bad"}`, `{"reviewed_destination":{}}`} {
		payout.DestinationSnapshot = []byte(raw)
		if _, err = service.recipientForPayout(context.Background(), payout, false); err == nil {
			t.Fatal("malformed snapshot accepted", raw)
		}
	}
	d.CurrencyCode = "NGN"
	d.IdentifierCiphertext[0] ^= 1
	payout.DestinationSnapshot = snapshot(d)
	if _, err = service.recipientForPayout(context.Background(), payout, false); err == nil {
		t.Fatal("invalid encrypted snapshot accepted")
	}
}
