package file

import (
	"encoding/json"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/common/model"
	"io/ioutil"
	"regexp"
	"sync"
	"time"
)

type ACLConfig struct {
	AllowList           map[string]string `json:"allow_list,omitempty"`
	BlockList           map[string]string `json:"block_list,omitempty"`
	FilterOptions       []FilterCase      `json:"filter_options,omitempty"`
	DefaultFilterOption FilterCase        `json:"default_filter"`
	ArchivedFormat      []string          `json:"archived_format"`
}

type LabelPair struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type FilterCase struct {
	Key         string   `json:"key,omitempty"`
	Value       string   `json:"value,omitempty"`
	ExcludePath []string `json:"exclude_path,omitempty"`
	Suffix      []string `json:"suffix,omitempty"`
}

type ACLManager struct {
	logger      log.Logger
	aclFilepath string
	cfg         *ACLConfig
	mux         sync.Mutex
}

func NewACLManager(logger log.Logger, aclFilepath string) *ACLManager {
	m := &ACLManager{
		logger:      logger,
		aclFilepath: aclFilepath,
		cfg:         &ACLConfig{},
		mux:         sync.Mutex{},
	}
	go m.sync()
	return m
}

func (m *ACLManager) sync() {
	ticker := time.NewTicker(time.Second * 10)
	for {
		select {
		case <-ticker.C:
			m.syncOnce()
		}
	}
}

func (m *ACLManager) syncOnce() {
	b, err := ioutil.ReadFile(m.aclFilepath)
	if err != nil {
		level.Debug(m.logger).Log("msg", "open acl file failed", "path", m.aclFilepath, "err", err.Error())
		return
	}
	cfg := ACLConfig{}
	err = json.Unmarshal(b, &cfg)
	if err != nil {
		level.Warn(m.logger).Log("msg", "invalid acl file format", "err", err.Error())
		return
	}
	m.mux.Lock()
	defer m.mux.Unlock()
	m.cfg = &cfg
}

func (m *ACLManager) IsAllow(labels model.LabelSet) bool {
	allow := true
	if m.cfg.AllowList != nil && len(m.cfg.AllowList) != 0 {
		allow = false
		for l, v := range m.cfg.AllowList {
			matched, err := regexp.MatchString(v, string(labels[model.LabelName(l)]))
			if err == nil && matched {
				allow = true
			}
		}
	}

	for l, v := range m.cfg.BlockList {
		matched, err := regexp.MatchString(v, string(labels[model.LabelName(l)]))
		if err == nil && matched {
			allow = false
		}
	}
	return allow
}

func (m *ACLManager) GetFilterOption(labels model.LabelSet) FilterCase {
	for _, f := range m.cfg.FilterOptions {
		if v, ok := labels[model.LabelName(f.Key)]; ok {
			matched, err := regexp.MatchString(f.Value, string(v))
			if err == nil && matched {
				return f
			}
		}
	}
	return m.cfg.DefaultFilterOption
}
