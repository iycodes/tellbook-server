package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/whatsapp"
)

func main() {
	scope := flag.String("scope", "all", "template contract scope: all or auth")
	flag.Parse()
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) && !errors.Is(err, os.ErrNotExist) {
		fail("load .env", err)
	}
	cfg, err := config.Load()
	if err != nil {
		fail("load configuration", err)
	}
	if !cfg.WhatsAppSendConfigured() {
		fail("validate configuration", errors.New("WhatsApp outbound configuration is required"))
	}
	client, err := whatsapp.NewClient(whatsapp.ClientConfig{
		BaseURL: cfg.WhatsAppGraphBaseURL, GraphVersion: cfg.WhatsAppGraphVersion,
		PhoneNumberID: cfg.WABAPhoneNumberID, BusinessAccountID: cfg.WhatsAppBusinessAccountID,
		AccessToken: cfg.WABAToken, Timeout: cfg.WhatsAppHTTPTimeout,
	})
	if err != nil {
		fail("configure WhatsApp Graph client", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.WhatsAppHTTPTimeout+5*time.Second)
	defer cancel()
	definitions := whatsapp.RegisteredTemplates()
	if *scope == "auth" {
		definition, ok := whatsapp.LookupTemplate(whatsapp.TemplateAuthCode)
		if !ok {
			fail("load auth template contract", errors.New("v_c_x is not registered"))
		}
		definitions = []whatsapp.TemplateDefinition{definition}
	} else if *scope != "all" {
		fail("validate scope", fmt.Errorf("unsupported template scope %q", *scope))
	} else if err := whatsapp.ValidateEnabledTemplateKeys(cfg.WhatsAppEnabledTemplateKeys); err != nil {
		fail("validate enabled templates", err)
	}
	report, err := client.ConformTemplateDefinitions(ctx, definitions)
	if err != nil {
		fail("check WhatsApp templates", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail("encode report", err)
	}
	_, _ = fmt.Fprintln(os.Stdout, string(encoded))
	if !report.Valid() {
		os.Exit(1)
	}
}

func fail(action string, err error) {
	_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", action, err)
	os.Exit(1)
}
