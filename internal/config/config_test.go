package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const yamlSecretLine = `secret: "yaml_secret_123456789012345678901234567890"`

const testYAML = `server:
  port: 8080
mysql:
  host: 127.0.0.1
  port: 3306
  user: root
  password: "yaml_mysql"
  dbname: forum
redis:
  addr: 127.0.0.1:6379
  password: "yaml_redis"
  db: 0
jwt:
  ` + yamlSecretLine + `
  expire: 24
`

func useTestConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", path)
	t.Setenv("JWT_SECRET", "")
	t.Setenv("MYSQL_PASSWORD", "")
	t.Setenv("MYSQL_AUTO_MIGRATE", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("SERVER_TRUSTED_PROXIES", "")
	t.Setenv("RATE_LIMIT_CAPACITY", "")
	t.Setenv("RATE_LIMIT_RATE", "")
	t.Setenv("RATE_LIMIT_KEY_PREFIX", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
}

func TestLoadFromYAML(t *testing.T) {
	useTestConfig(t, testYAML)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQL.Password != "yaml_mysql" {
		t.Fatalf("mysql password = %q", cfg.MySQL.Password)
	}
	if cfg.Redis.Password != "yaml_redis" {
		t.Fatalf("redis password = %q", cfg.Redis.Password)
	}
	if cfg.JWT.Secret != "yaml_secret_123456789012345678901234567890" {
		t.Fatalf("jwt secret = %q", cfg.JWT.Secret)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	useTestConfig(t, testYAML)
	t.Setenv("JWT_SECRET", "env_secret_123456789012345678901234567890")
	t.Setenv("MYSQL_PASSWORD", "env_mysql")
	t.Setenv("REDIS_PASSWORD", "env_redis")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWT.Secret != "env_secret_123456789012345678901234567890" {
		t.Fatalf("JWT_SECRET did not override, got %q", cfg.JWT.Secret)
	}
	if cfg.MySQL.Password != "env_mysql" {
		t.Fatalf("MYSQL_PASSWORD did not override, got %q", cfg.MySQL.Password)
	}
	if cfg.Redis.Password != "env_redis" {
		t.Fatalf("REDIS_PASSWORD did not override, got %q", cfg.Redis.Password)
	}
}

func TestLoadRejectsWeakSecret(t *testing.T) {
	weak := []string{"", "short", "change_me_to_a_long_random_string", "replace_with_a_long_random_secret"}
	for _, secret := range weak {
		content := strings.Replace(testYAML, yamlSecretLine, fmt.Sprintf("secret: %q", secret), 1)
		useTestConfig(t, content)
		if _, err := Load(); err == nil {
			t.Fatalf("secret %q should be rejected", secret)
		}
	}
}

func TestLoadWSOrigins(t *testing.T) {
	useTestConfig(t, testYAML+`
ws:
  allowed_origins:
    - "127.0.0.1:5173"
    - "tunnel.example.com"
`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WS.AllowedOrigins) != 2 {
		t.Fatalf("allowed origins = %v", cfg.WS.AllowedOrigins)
	}
	if cfg.WS.AllowedOrigins[0] != "127.0.0.1:5173" || cfg.WS.AllowedOrigins[1] != "tunnel.example.com" {
		t.Fatalf("allowed origins = %v", cfg.WS.AllowedOrigins)
	}
}

func TestLoadRateLimitDefaults(t *testing.T) {
	useTestConfig(t, testYAML)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimit.Capacity != 10 || cfg.RateLimit.Rate != 2 || cfg.RateLimit.KeyPrefix != "forum:dev:rate:" {
		t.Fatalf("rate limit defaults = %+v", cfg.RateLimit)
	}
	if len(cfg.Server.TrustedProxies) != 0 {
		t.Fatalf("trusted proxies should be empty, got %v", cfg.Server.TrustedProxies)
	}
}

func TestLoadRateLimitFromYAML(t *testing.T) {
	useTestConfig(t, testYAML+`
rate_limit:
  capacity: 25
  rate: 5
  key_prefix: "forum:test:rate:"
`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimit.Capacity != 25 || cfg.RateLimit.Rate != 5 || cfg.RateLimit.KeyPrefix != "forum:test:rate:" {
		t.Fatalf("rate limit = %+v", cfg.RateLimit)
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	content := strings.Replace(testYAML, "  port: 8080", "  port: 8080\n  trusted_proxies:\n    - \"127.0.0.1\"\n    - \"10.0.0.0/8\"", 1)
	useTestConfig(t, content)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Server.TrustedProxies) != 2 || cfg.Server.TrustedProxies[0] != "127.0.0.1" || cfg.Server.TrustedProxies[1] != "10.0.0.0/8" {
		t.Fatalf("trusted proxies = %v", cfg.Server.TrustedProxies)
	}
}

func TestLoadRateLimitEnvOverride(t *testing.T) {
	useTestConfig(t, testYAML)
	t.Setenv("RATE_LIMIT_CAPACITY", "50")
	t.Setenv("RATE_LIMIT_RATE", "8")
	t.Setenv("RATE_LIMIT_KEY_PREFIX", "forum:env:rate:")
	t.Setenv("SERVER_TRUSTED_PROXIES", "127.0.0.1,10.0.0.0/8")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimit.Capacity != 50 || cfg.RateLimit.Rate != 8 || cfg.RateLimit.KeyPrefix != "forum:env:rate:" {
		t.Fatalf("rate limit = %+v", cfg.RateLimit)
	}
	if len(cfg.Server.TrustedProxies) != 2 {
		t.Fatalf("trusted proxies = %v", cfg.Server.TrustedProxies)
	}
}

func TestLoadCORSDefaults(t *testing.T) {
	useTestConfig(t, testYAML)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORS.AllowedOrigins) != 0 {
		t.Fatalf("CORS should be off by default, got %v", cfg.CORS.AllowedOrigins)
	}
}

func TestLoadCORSOriginsFromYAML(t *testing.T) {
	useTestConfig(t, testYAML+`
cors:
  allowed_origins:
    - "https://front.example.com"
    - "http://127.0.0.1:5173"
`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORS.AllowedOrigins) != 2 || cfg.CORS.AllowedOrigins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("allowed origins = %v", cfg.CORS.AllowedOrigins)
	}
}

func TestLoadCORSEnvOverride(t *testing.T) {
	useTestConfig(t, testYAML)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example.com,https://b.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORS.AllowedOrigins) != 2 || cfg.CORS.AllowedOrigins[0] != "https://a.example.com" {
		t.Fatalf("allowed origins = %v", cfg.CORS.AllowedOrigins)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "missing.yaml"))

	if _, err := Load(); err == nil {
		t.Fatal("missing config file should return an error")
	}
}

// yaml 里没写 auto_migrate 时默认开，本机开发改完模型重启就能建表。
func TestLoadAutoMigrateDefaultsTrue(t *testing.T) {
	useTestConfig(t, testYAML)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MySQL.AutoMigrate {
		t.Fatal("auto_migrate should default to true")
	}
}

// 生产模板靠这一项关掉自动建表，两种写法（yaml 与环境变量）都要生效。
func TestLoadAutoMigrateCanBeDisabled(t *testing.T) {
	content := strings.Replace(testYAML, "  dbname: forum", "  dbname: forum\n  auto_migrate: false", 1)
	useTestConfig(t, content)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQL.AutoMigrate {
		t.Fatal("auto_migrate: false in yaml should be respected")
	}

	useTestConfig(t, testYAML)
	t.Setenv("MYSQL_AUTO_MIGRATE", "false")

	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQL.AutoMigrate {
		t.Fatal("MYSQL_AUTO_MIGRATE=false should override the default")
	}
}
