package payaza

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"booking/go-server/internal/payments"
)

const DefaultBaseURL = "https://api.payaza.africa/live"

type Config struct {
	PublicKey          string
	SecretKey          string
	BaseURL            string
	TenantID           string
	TransactionPIN     string
	DVABankCode        string
	DVAEnquiryBankCode string
	DVABankName        string
	SourceAccounts     map[string]string
	PayoutSender       PayoutSender
	HTTPClient         *http.Client
}

type PayoutSender struct {
	Name    string
	Phone   string
	Address string
}

var (
	_ payments.CollectionProvider      = (*Client)(nil)
	_ payments.WebhookVerifier         = (*Client)(nil)
	_ payments.DestinationProvider     = (*Client)(nil)
	_ payments.PayoutProvider          = (*Client)(nil)
	_ payments.PayoutLiquidityProvider = (*Client)(nil)
)

type Client struct {
	publicKey          string
	secretKey          string
	baseURL            string
	tenantID           string
	transactionPIN     string
	dvaBankCode        string
	dvaEnquiryBankCode string
	dvaBankName        string
	sourceAccounts     map[string]string
	payoutSender       PayoutSender
	httpClient         *http.Client
}

type ErrorResponse struct {
	HTTPStatus int
	Message    string
}

func (e *ErrorResponse) Error() string {
	return fmt.Sprintf("payaza: http=%d message=%s", e.HTTPStatus, e.Message)
}

func (e *ErrorResponse) HTTPStatusCode() int { return e.HTTPStatus }

func NewClient(cfg Config) (*Client, error) {
	publicKey := strings.TrimSpace(cfg.PublicKey)
	secretKey := strings.TrimSpace(cfg.SecretKey)
	tenantID := strings.ToLower(strings.TrimSpace(cfg.TenantID))
	if publicKey == "" || secretKey == "" {
		return nil, errors.New("payaza public and secret keys are required")
	}
	if tenantID != "test" && tenantID != "live" {
		return nil, errors.New("payaza tenant ID must be test or live")
	}
	dvaBankCode := strings.TrimSpace(cfg.DVABankCode)
	dvaEnquiryBankCode := strings.TrimSpace(cfg.DVAEnquiryBankCode)
	dvaBankName := strings.TrimSpace(cfg.DVABankName)
	if dvaBankCode != "" || dvaEnquiryBankCode != "" || dvaBankName != "" {
		if (dvaBankCode != "1067" && dvaBankCode != "140") || dvaEnquiryBankCode == "" || dvaBankName == "" {
			return nil, errors.New("payaza DVA bank configuration is invalid")
		}
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	sourceAccounts := make(map[string]string, len(cfg.SourceAccounts))
	for currency, reference := range cfg.SourceAccounts {
		currency = strings.ToUpper(strings.TrimSpace(currency))
		reference = strings.TrimSpace(reference)
		if len(currency) != 3 || reference == "" {
			return nil, errors.New("invalid payaza source account configuration")
		}
		sourceAccounts[currency] = reference
	}
	return &Client{
		publicKey: publicKey, secretKey: secretKey, baseURL: baseURL, tenantID: tenantID,
		transactionPIN: strings.TrimSpace(cfg.TransactionPIN), sourceAccounts: sourceAccounts,
		dvaBankCode: dvaBankCode, dvaEnquiryBankCode: dvaEnquiryBankCode, dvaBankName: dvaBankName,
		payoutSender: PayoutSender{
			Name: strings.TrimSpace(cfg.PayoutSender.Name), Phone: strings.TrimSpace(cfg.PayoutSender.Phone),
			Address: strings.TrimSpace(cfg.PayoutSender.Address),
		},
		httpClient: httpClient,
	}, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, tenant bool, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode payaza request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Payaza "+base64.StdEncoding.EncodeToString([]byte(c.publicKey)))
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if tenant {
		request.Header.Set("X-TenantID", c.tenantID)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Message         string `json:"message"`
			ResponseMessage string `json:"response_message"`
		}
		_ = json.Unmarshal(responseBody, &failure)
		message := strings.TrimSpace(failure.Message)
		if message == "" {
			message = strings.TrimSpace(failure.ResponseMessage)
		}
		return &ErrorResponse{HTTPStatus: response.StatusCode, Message: message}
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode payaza response: %w", err)
	}
	return nil
}
