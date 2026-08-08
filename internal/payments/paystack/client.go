package paystackclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.paystack.co"
	listBanksPath  = "/bank"
	resolvePath    = "/bank/resolve"
	maxBankPages   = 10
)

type Config struct {
	SecretKey  string
	BaseURL    string
	HTTPClient *http.Client
}

type Client struct {
	secretKey  string
	baseURL    string
	httpClient *http.Client
}

type Bank struct {
	Name      string `json:"name"`
	Code      string `json:"code"`
	Active    bool   `json:"active"`
	IsDeleted bool   `json:"is_deleted"`
}

type AccountResolution struct {
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
}

type responseEnvelope[T any] struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    T      `json:"data"`
	Meta    struct {
		Next      string `json:"next"`
		Page      int    `json:"page"`
		PageCount int    `json:"pageCount"`
	} `json:"meta"`
}

type ErrorResponse struct {
	HTTPStatus int
	Message    string
	Body       []byte
}

func (e *ErrorResponse) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message != "" {
		return fmt.Sprintf("paystack: http=%d message=%s", e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("paystack: http=%d", e.HTTPStatus)
}

func (e *ErrorResponse) HTTPStatusCode() int { return e.HTTPStatus }

func NewClient(cfg Config) (*Client, error) {
	secretKey := strings.TrimSpace(cfg.SecretKey)
	if secretKey == "" {
		return nil, errors.New("secret key is required")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	return &Client{
		secretKey:  secretKey,
		baseURL:    baseURL,
		httpClient: httpClient,
	}, nil
}

func (c *Client) ListBanks(ctx context.Context) ([]Bank, error) {
	return c.ListBanksFor(ctx, "nigeria", "NGN", "nuban")
}

func (c *Client) ListBanksFor(ctx context.Context, country, currency, bankType string) ([]Bank, error) {
	banks := make([]Bank, 0, 100)
	next := ""
	seenCursors := make(map[string]struct{})

	for page := 0; page < maxBankPages; page++ {
		query := url.Values{
			"country":    {strings.TrimSpace(country)},
			"currency":   {strings.ToUpper(strings.TrimSpace(currency))},
			"type":       {strings.TrimSpace(bankType)},
			"use_cursor": {"true"},
			"perPage":    {"100"},
		}
		if next != "" {
			query.Set("next", next)
		}

		response, err := doGet[[]Bank](ctx, c, listBanksPath, query)
		if err != nil {
			return nil, err
		}
		banks = append(banks, response.Data...)

		next = strings.TrimSpace(response.Meta.Next)
		if next == "" {
			return banks, nil
		}
		if _, exists := seenCursors[next]; exists {
			return nil, errors.New("paystack: repeated bank-list cursor")
		}
		seenCursors[next] = struct{}{}
	}

	return nil, errors.New("paystack: bank list exceeded pagination limit")
}

func (c *Client) ResolveAccount(ctx context.Context, bankCode, accountNumber string) (*AccountResolution, error) {
	query := url.Values{
		"bank_code":      {strings.TrimSpace(bankCode)},
		"account_number": {strings.TrimSpace(accountNumber)},
	}
	response, err := doGet[AccountResolution](ctx, c, resolvePath, query)
	if err != nil {
		return nil, err
	}
	return &response.Data, nil
}

func doGet[T any](ctx context.Context, c *Client, path string, query url.Values) (responseEnvelope[T], error) {
	var zero responseEnvelope[T]

	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return zero, err
	}
	request.Header.Set("Authorization", "Bearer "+c.secretKey)
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return zero, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return zero, err
	}

	var envelope responseEnvelope[T]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return zero, fmt.Errorf("decode paystack response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Status {
		return zero, &ErrorResponse{
			HTTPStatus: response.StatusCode,
			Message:    envelope.Message,
			Body:       body,
		}
	}

	return envelope, nil
}
