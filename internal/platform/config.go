package platform

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL          string
	MasterKey            []byte
	SessionKey           []byte
	Port                 string
	WebDir               string
	ProjectRoot          string
	RuntimeImage         string
	RuntimeCPU           int64
	RuntimeMemoryBytes   int64
	RuntimePIDs          int64
	RuntimeIdleTTL       time.Duration
	RuntimeSweepInterval time.Duration
	DataVolumeName       string
	RuntimeNetwork       string
	DeployPortBase       int
	DeployPortSpan       int
}

func LoadConfig() (Config, error) {
	key, err := requiredKey("APP_MASTER_KEY")
	if err != nil {
		return Config{}, err
	}
	sessionKey, err := requiredKey("APP_SESSION_KEY")
	if err != nil {
		return Config{}, err
	}
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}
	webDir := os.Getenv("WEB_DIR")
	if webDir == "" {
		webDir = "web/dist"
	}
	projectRoot := os.Getenv("PROJECT_ROOT")
	if projectRoot == "" {
		projectRoot = "/data/users"
	}
	image := os.Getenv("RUNTIME_IMAGE")
	if image == "" {
		image = "atoms-runtime:latest"
	}
	volume := os.Getenv("DATA_VOLUME_NAME")
	if volume == "" {
		volume = "atoms-data"
	}
	network := os.Getenv("RUNTIME_NETWORK")
	if network == "" {
		network = "atoms-internal"
	}
	deployPortBase := envInt("DEPLOY_PORT_BASE", 18000)
	deployPortSpan := envInt("DEPLOY_PORT_SPAN", 5)
	if deployPortSpan < maxProjectsPerUser {
		return Config{}, fmt.Errorf("DEPLOY_PORT_SPAN must be at least %d", maxProjectsPerUser)
	}
	if deployPortBase > 65535 || deployPortSpan > 65535-deployPortBase+1 {
		return Config{}, fmt.Errorf("deployment port range must stay within 1-65535")
	}
	return Config{DatabaseURL: db, MasterKey: key, SessionKey: sessionKey, Port: port, WebDir: webDir, ProjectRoot: projectRoot, RuntimeImage: image, RuntimeCPU: 2, RuntimeMemoryBytes: 2 << 30, RuntimePIDs: 256, RuntimeIdleTTL: 24 * time.Hour, RuntimeSweepInterval: 15 * time.Minute, DataVolumeName: volume, RuntimeNetwork: network, DeployPortBase: deployPortBase, DeployPortSpan: deployPortSpan}, nil
}

func envInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func requiredKey(name string) ([]byte, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must be base64-encoded 32 bytes", name)
	}
	return key, nil
}
