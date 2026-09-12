package platform

import (
	"encoding/base64"
	"fmt"
	"os"
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
	return Config{DatabaseURL: db, MasterKey: key, SessionKey: sessionKey, Port: port, WebDir: webDir, ProjectRoot: projectRoot, RuntimeImage: image, RuntimeCPU: 2, RuntimeMemoryBytes: 2 << 30, RuntimePIDs: 256, RuntimeIdleTTL: 24 * time.Hour, RuntimeSweepInterval: 15 * time.Minute, DataVolumeName: volume, RuntimeNetwork: network}, nil
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
