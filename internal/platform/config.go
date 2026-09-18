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
	PreviewPortRange     string
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
	previewPortRange := os.Getenv("PREVIEW_PORT_RANGE")
	previewPorts, err := parsePreviewPorts(previewPortRange)
	if err != nil {
		return Config{}, err
	}
	for _, previewPort := range previewPorts {
		if previewPort == port {
			return Config{}, fmt.Errorf("preview ports overlap APP_PORT")
		}
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
	if deployPortBase < 1 || deployPortBase > 65535 || deployPortSpan > 65535-deployPortBase+1 {
		return Config{}, fmt.Errorf("deployment port range must stay within 1-65535")
	}
	for _, previewPort := range previewPorts {
		number, _ := strconv.Atoi(previewPort)
		if number >= deployPortBase {
			return Config{}, fmt.Errorf("PREVIEW_PORT_RANGE must be below DEPLOY_PORT_BASE")
		}
	}
	return Config{DatabaseURL: db, MasterKey: key, SessionKey: sessionKey, Port: port, PreviewPortRange: previewPortRange, WebDir: webDir, ProjectRoot: projectRoot, RuntimeImage: image, RuntimeCPU: 2, RuntimeMemoryBytes: 2 << 30, RuntimePIDs: 256, RuntimeIdleTTL: 24 * time.Hour, RuntimeSweepInterval: 15 * time.Minute, DataVolumeName: volume, RuntimeNetwork: network, DeployPortBase: deployPortBase, DeployPortSpan: deployPortSpan}, nil
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
