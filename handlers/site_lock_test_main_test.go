package handlers

import (
	"os"
	"testing"

	"github.com/naibabiji/wp-panel/config"
)

func TestMain(m *testing.M) {
	dataDir, err := os.MkdirTemp("", "wp-panel-handlers-test-")
	if err != nil {
		panic(err)
	}
	config.AppConfig = &config.Config{Panel: config.PanelConfig{DataDir: dataDir}}
	code := m.Run()
	_ = os.RemoveAll(dataDir)
	os.Exit(code)
}
