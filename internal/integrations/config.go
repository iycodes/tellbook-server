package integrations

import (
	"fmt"
	"github.com/google/uuid"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Client struct {
	MetadataURL string
	RedirectURI string
}
type Config struct {
	Enabled       bool
	WritesEnabled bool
	PublicURL     string
	ClientURL     string
	AllProviders  bool
	Providers     map[uuid.UUID]bool
	Clients       map[string]Client
}

func LoadConfig(clientURL string) (Config, error) {
	enabled, err := integrationBool("INTEGRATIONS_ENABLED")
	if err != nil {
		return Config{}, err
	}
	writes, err := integrationBool("INTEGRATIONS_WRITES_ENABLED")
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Enabled: enabled, WritesEnabled: writes, PublicURL: strings.TrimRight(os.Getenv("INTEGRATIONS_PUBLIC_BASE_URL"), "/"), ClientURL: strings.TrimRight(clientURL, "/"), Providers: map[uuid.UUID]bool{}, Clients: map[string]Client{}}
	selection := strings.TrimSpace(os.Getenv("INTEGRATIONS_PROVIDER_ALLOWLIST"))
	if strings.EqualFold(selection, "all") {
		cfg.AllProviders = true
	} else {
		for _, raw := range strings.Split(selection, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if strings.EqualFold(raw, "all") {
				return cfg, fmt.Errorf("INTEGRATIONS_PROVIDER_ALLOWLIST must use all by itself or a comma-separated list of provider UUIDs")
			}
			id, e := uuid.Parse(raw)
			if e != nil {
				return cfg, fmt.Errorf("INTEGRATIONS_PROVIDER_ALLOWLIST contains an invalid provider ID")
			}
			cfg.Providers[id] = true
		}
	}
	cfg.Clients["chatgpt"] = Client{envDefault("INTEGRATIONS_CHATGPT_CLIENT_METADATA_URL", "https://chatgpt.com/oauth/client.json"), envDefault("INTEGRATIONS_CHATGPT_REDIRECT_URI", "https://chatgpt.com/connector_platform_oauth_redirect")}
	cfg.Clients["claude"] = Client{os.Getenv("INTEGRATIONS_CLAUDE_CLIENT_METADATA_URL"), "https://claude.ai/api/mcp/auth_callback"}
	if !enabled {
		return cfg, nil
	}
	for name, raw := range map[string]string{"INTEGRATIONS_PUBLIC_BASE_URL": cfg.PublicURL, "CLIENT_PUBLIC_BASE_URL": cfg.ClientURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return cfg, fmt.Errorf("%s must be an HTTPS origin", name)
		}
	}
	if !cfg.AllProviders && len(cfg.Providers) == 0 {
		return cfg, fmt.Errorf("INTEGRATIONS_PROVIDER_ALLOWLIST must be all or a comma-separated list of provider UUIDs when integrations are enabled")
	}
	for platform, client := range cfg.Clients {
		if !officialURL(platform, client.MetadataURL) || !officialURL(platform, client.RedirectURI) {
			return cfg, fmt.Errorf("configure official %s client metadata and redirect URLs", platform)
		}
	}
	return cfg, nil
}
func integrationBool(name string) (bool, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return false, nil
	}
	v, e := strconv.ParseBool(raw)
	if e != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return v, nil
}
func envDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func officialURL(platform, raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Port() != "" {
		return false
	}
	host := "chatgpt.com"
	if platform == "claude" {
		host = "claude.ai"
	}
	return u.Host == host && u.Path != ""
}
