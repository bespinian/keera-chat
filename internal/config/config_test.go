package config

import (
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func base() map[string]string {
	return map[string]string{
		"KEERA_CHAT_GATEWAY_URL": "https://keera.example.ch/",
		"KEERA_CHAT_SECRET_KEY":  "0123456789abcdef0123",
	}
}

func TestDefaults(t *testing.T) {
	c, err := load(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if c.GatewayURL != "https://keera.example.ch" || c.GatewayInternalURL != c.GatewayURL {
		t.Errorf("gateway = %q, internal %q", c.GatewayURL, c.GatewayInternalURL)
	}
	// The gateway allows loopback without setup.
	if c.PublicURL != "http://127.0.0.1:8090" || c.SecureCookies {
		t.Errorf("public URL = %q, secure = %v", c.PublicURL, c.SecureCookies)
	}
}

func TestHTTPSIsSecure(t *testing.T) {
	kv := base()
	kv["KEERA_CHAT_PUBLIC_URL"] = "https://chat.example.ch"
	c, err := load(env(kv))
	if err != nil || !c.SecureCookies {
		t.Fatalf("secure = %v, %v", c.SecureCookies, err)
	}
	kv["KEERA_CHAT_SECURE_COOKIES"] = "false"
	if c, _ = load(env(kv)); c.SecureCookies {
		t.Fatal("KEERA_CHAT_SECURE_COOKIES=false was ignored")
	}
}

func TestRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		key, value, want string
	}{
		"no gateway":       {"KEERA_CHAT_GATEWAY_URL", "", "KEERA_CHAT_GATEWAY_URL is required"},
		"gateway path":     {"KEERA_CHAT_GATEWAY_URL", "https://example.ch/keera", "without a path"},
		"gateway scheme":   {"KEERA_CHAT_GATEWAY_URL", "ftp://example.ch", "http or https"},
		"short key":        {"KEERA_CHAT_SECRET_KEY", "short", "at least 16"},
		"public path":      {"KEERA_CHAT_PUBLIC_URL", "https://example.ch/chat", "without a path"},
		"secure cookies":   {"KEERA_CHAT_SECURE_COOKIES", "maybe", "true or false"},
		"tiny body":        {"KEERA_CHAT_MAX_BODY_BYTES", "100", "at least"},
		"no port, no URL":  {"KEERA_CHAT_ADDR", "unix-socket", "KEERA_CHAT_PUBLIC_URL is required"},
		"internal garbage": {"KEERA_CHAT_GATEWAY_INTERNAL_URL", "gateway:8080", "KEERA_CHAT_GATEWAY_INTERNAL_URL"},
		"log level":        {"KEERA_CHAT_LOG_LEVEL", "verbose", "debug, info, warn or error"},
		"log format":       {"KEERA_CHAT_LOG_FORMAT", "yaml", "text or json"},
	} {
		kv := base()
		kv[tc.key] = tc.value
		_, err := load(env(kv))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
		}
	}
}
