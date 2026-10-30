// Package config 是全部配置的唯一定义与加载点。
// 配置仅来自 YAML 文件 configs/config.yaml，不读取环境变量。
// 首次启动未找到配置文件时：自动生成含全部默认项的配置文件并返回
// ErrConfigFileCreated，由 main 打印提示后退出，用户修改后再重启。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// 支持的数据库驱动（纯 Go，保证 CGO_ENABLED=0 交叉编译）。
// DriverNone 表示不启用数据库——数据库是可选组件，不需要持久化时
// 把 database.driver 设为 none，程序即为纯 HTTP + embed 的最小闭环。
const (
	DriverNone     = "none"
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
	DriverOracle   = "oracle"
)

// 出站目标校验策略（与 internal/client.SSRFPolicy 取值一致，config 不反向依赖 client）。
const (
	SSRFDenyPrivate  = "deny_private"
	SSRFAllowPrivate = "allow_private"
	SSRFAllowHosts   = "allow_hosts"
)

// 判断是否启用了数据库。
func (d DatabaseConfig) Enabled() bool { return d.Driver != DriverNone }

// DefaultConfigPath 配置文件固定路径（相对工作目录）。
const DefaultConfigPath = "configs/config.yaml"

// ErrConfigFileCreated 表示默认配置文件刚刚生成，程序应打印提示后退出。
var ErrConfigFileCreated = errors.New("config file created")

type Config struct {
	HTTP     HTTPConfig     `yaml:"http"`
	Database DatabaseConfig `yaml:"database"`
	Engine   EngineConfig   `yaml:"engine"`
	Client   ClientConfig   `yaml:"client"`
	Log      LogConfig      `yaml:"log"`
}

// EngineConfig 多执行引擎的调度参数。
type EngineConfig struct {
	MaxConcurrent int `yaml:"max_concurrent"` // 同时执行上限，满则返回 429（不排队，避免堆积）
	TimeoutSec    int `yaml:"timeout_s"`      // 单次执行超时
}

// ClientConfig 外部依赖（HTTP/MQTT/MQ/Redis）的连接与重试参数。
type ClientConfig struct {
	HTTPTimeoutSec int `yaml:"http_timeout_s"` // 单次 HTTP 请求超时
	HTTPRetries    int `yaml:"http_retries"`   // 幂等请求重试次数，0 表示不重试
	// SSRFPolicy 出站目标校验策略：
	//   deny_private  拒绝回环/私网（适用于 URL 来自用户输入）
	//   allow_private 允许私网与回环（工业内网/私有云里的可信上游）
	//   allow_hosts   只允许 SSRFAllowHosts 白名单内的主机/IP 前缀/CIDR
	// 云元数据地址（169.254.169.254 等）在任何策略下都会被拒绝。
	SSRFPolicy     string   `yaml:"ssrf_policy"`
	SSRFAllowHosts []string `yaml:"ssrf_allow_hosts"`
}

type HTTPConfig struct {
	Addr string `yaml:"addr"`
	// EmbedFrontend=true：内嵌前端（单二进制形态，前端产物在 internal/webfs/dist）。
	// false：前后端分离形态，本进程只提供 API，未匹配路径返回 JSON 404。
	EmbedFrontend bool `yaml:"embed_frontend"`
	// AllowedOrigins 跨域白名单。embed 形态同源部署时无实际作用；
	// 前后端分离形态必须填入前端域名，生产禁止使用 "*"。
	AllowedOrigins []string `yaml:"allowed_origins"`
	// AllowCredentials 是否允许跨域携带 Cookie/凭证。
	// 为 true 时后端必须回显具体 Origin，不能回 "*"（浏览器会拒绝）。
	AllowCredentials bool `yaml:"allow_credentials"`
}

type DatabaseConfig struct {
	Driver             string `yaml:"driver"`         // none | sqlite | postgres | oracle；none 表示不启用数据库
	DSN                string `yaml:"dsn"`            // 留空则按驱动取默认值（driver=none 时忽略）
	MaxOpenConns       int    `yaml:"max_open_conns"` // SQLite 遇 busy 可设为 1
	MaxIdleConns       int    `yaml:"max_idle_conns"`
	ConnMaxLifetimeSec int    `yaml:"conn_max_lifetime_s"` // 连接最大复用时长（秒）
}

type LogConfig struct {
	Dir            string `yaml:"dir"`                  // 日志目录
	Level          string `yaml:"level"`                // debug | info | warn | error
	Rotate         string `yaml:"rotate"`               // daily：默认按天轮转 app-YYYY-MM-DD.log
	RetentionDays  int    `yaml:"retention_days"`       // 日志保留天数，超期自动清理
	CleanIntervalH int    `yaml:"clean_interval_hours"` // 清理扫描频率（小时），不必太高
}

// Load 加载配置。未找到配置文件时生成默认配置文件，返回包装了
// ErrConfigFileCreated 的错误；main 据此打印提示并正常退出。
func Load() (*Config, error) {
	b, err := os.ReadFile(DefaultConfigPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if gerr := generateDefault(); gerr != nil {
				return nil, fmt.Errorf("generate default config: %w", gerr)
			}
			return nil, fmt.Errorf("%w: %s", ErrConfigFileCreated, DefaultConfigPath)
		}
		return nil, fmt.Errorf("read config file %s: %w", DefaultConfigPath, err)
	}

	cfg := defaults()
	if err := checkStructure(b); err != nil {
		return nil, fmt.Errorf("config file %s: %w", DefaultConfigPath, err)
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", DefaultConfigPath, err)
	}
	fillDefaults(cfg)
	return cfg, nil
}

// checkStructure 校验顶层与各节均为映射，避免缩进错误时静默回退默认值。
func checkStructure(b []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("invalid yaml: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return errors.New("empty document")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return errors.New("top level must be a mapping")
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, val := doc.Content[i], doc.Content[i+1]
		if val.Kind != yaml.MappingNode {
			return fmt.Errorf("section %q must be a mapping (check indentation)", key.Value)
		}
	}
	return nil
}

// defaults 返回带初始值的配置（未被配置文件覆盖的字段保持默认）。
func defaults() *Config {
	return &Config{
		HTTP: HTTPConfig{
			Addr:             ":8080",
			EmbedFrontend:    true,
			AllowedOrigins:   []string{"*"},
			AllowCredentials: false,
		},
		Database: DatabaseConfig{Driver: DriverSQLite},
		Engine:   EngineConfig{MaxConcurrent: 4, TimeoutSec: 30},
		Client:   ClientConfig{HTTPTimeoutSec: 10, HTTPRetries: 2},
		Log: LogConfig{
			Dir:            "logs",
			Level:          "info",
			Rotate:         "daily",
			RetentionDays:  7,
			CleanIntervalH: 24,
		},
	}
}

// generateDefault 生成包含全部默认项的配置文件，方便用户按需修改。
func generateDefault() error {
	if err := os.MkdirAll(filepath.Dir(DefaultConfigPath), 0o755); err != nil {
		return err
	}
	cfg := defaults()
	fillDefaults(cfg)
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(DefaultConfigPath, b, 0o644)
}

// fillDefaults 补齐未显式配置的项。
func fillDefaults(cfg *Config) {
	if cfg.HTTP.Addr == "" {
		cfg.HTTP.Addr = ":8080"
	}
	if len(cfg.HTTP.AllowedOrigins) == 0 {
		cfg.HTTP.AllowedOrigins = []string{"*"}
	}
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = DriverSQLite
	}
	if cfg.Database.DSN == "" && cfg.Database.Driver != DriverNone {
		cfg.Database.DSN = defaultDSN(cfg.Database.Driver)
	}
	if cfg.Database.MaxOpenConns == 0 {
		cfg.Database.MaxOpenConns = 10
	}
	if cfg.Database.MaxIdleConns == 0 {
		cfg.Database.MaxIdleConns = 5
	}
	if cfg.Database.ConnMaxLifetimeSec == 0 {
		cfg.Database.ConnMaxLifetimeSec = 300
	}
	if cfg.Engine.MaxConcurrent == 0 {
		cfg.Engine.MaxConcurrent = 4
	}
	if cfg.Engine.TimeoutSec == 0 {
		cfg.Engine.TimeoutSec = 30
	}
	if cfg.Client.HTTPTimeoutSec == 0 {
		cfg.Client.HTTPTimeoutSec = 10
	}
	if cfg.Client.HTTPRetries < 0 {
		cfg.Client.HTTPRetries = 0
	}
	if cfg.Client.SSRFPolicy == "" {
		cfg.Client.SSRFPolicy = SSRFDenyPrivate
	}
	if cfg.Log.Dir == "" {
		cfg.Log.Dir = "logs"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Rotate == "" {
		cfg.Log.Rotate = "daily"
	}
	if cfg.Log.RetentionDays == 0 {
		cfg.Log.RetentionDays = 7
	}
	if cfg.Log.CleanIntervalH == 0 {
		cfg.Log.CleanIntervalH = 24
	}
}

func defaultDSN(driver string) string {
	switch driver {
	case DriverPostgres:
		return "postgres://postgres:postgres@localhost:5432/app?sslmode=disable"
	case DriverOracle:
		return "oracle://app:app@localhost:1521/FREEPDB1"
	default: // SQLite：单文件 + WAL + 忙等待，适合小型应用
		return "file:data/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
}

// Validate 启动即校验（fail fast），必需项缺失直接拒绝启动。
// 注意：数据库是可选组件，driver: none 时跳过 DSN 校验。
func (c *Config) Validate() error {
	switch c.Database.Driver {
	case DriverNone:
		// 不启用数据库：无持久化需求，跳过 DSN 与连接池校验
	case DriverSQLite, DriverPostgres, DriverOracle:
		if c.Database.DSN == "" {
			return fmt.Errorf("database dsn is required")
		}
	default:
		return fmt.Errorf("unsupported database driver: %q (want none|sqlite|postgres|oracle)", c.Database.Driver)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level: %q (want debug|info|warn|error)", c.Log.Level)
	}
	if c.Log.Rotate != "daily" {
		return fmt.Errorf("invalid log rotate: %q (only \"daily\" is supported)", c.Log.Rotate)
	}
	if c.Engine.MaxConcurrent < 1 {
		return fmt.Errorf("engine.max_concurrent must be >= 1")
	}
	if c.Engine.TimeoutSec < 1 {
		return fmt.Errorf("engine.timeout_s must be >= 1")
	}
	if c.Client.HTTPTimeoutSec < 1 {
		return fmt.Errorf("client.http_timeout_s must be >= 1")
	}
	switch c.Client.SSRFPolicy {
	case SSRFDenyPrivate, SSRFAllowPrivate:
	case SSRFAllowHosts:
		if len(c.Client.SSRFAllowHosts) == 0 {
			return fmt.Errorf("client.ssrf_policy=%s requires a non-empty client.ssrf_allow_hosts", SSRFAllowHosts)
		}
	default:
		return fmt.Errorf("invalid client.ssrf_policy: %q (want deny_private|allow_private|allow_hosts)", c.Client.SSRFPolicy)
	}
	// CORS：白名单不能有空项；带凭证时不能使用 "*"（浏览器会直接拒绝，且属越权风险）
	allowAll := false
	for _, o := range c.HTTP.AllowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			return fmt.Errorf("http.allowed_origins contains an empty entry")
		}
		if o == "*" {
			allowAll = true
		}
	}
	if allowAll && c.HTTP.AllowCredentials {
		return fmt.Errorf("http.allow_credentials must not be true with wildcard origin %q: list concrete origins instead", "*")
	}
	return nil
}
