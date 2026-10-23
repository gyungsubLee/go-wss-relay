// Package coordinator owns single-writer room placement and node reconciliation.
package coordinator

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

type NodeConfig struct {
	ID           string `json:"id"`
	ControlURL   string `json:"controlUrl"`
	WebSocketURL string `json:"websocketUrl"`
	TokenFile    string `json:"tokenFile"`
	CAFile       string `json:"caFile,omitempty"`
}

func ReadConfig(path string) ([]NodeConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var config struct {
		Nodes []NodeConfig `json:"nodes"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("invalid trailing node configuration")
	}
	if len(config.Nodes) == 0 || len(config.Nodes) > 64 {
		return nil, errors.New("configure 1..64 nodes")
	}
	return config.Nodes, nil
}

func New(configs []NodeConfig) (*Pool, error) {
	pool := &Pool{nodes: make(map[string]*node), owners: make(map[string]*ownedRoom), now: time.Now}
	for _, config := range configs {
		if err := pool.Register(config); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return pool, nil
}
