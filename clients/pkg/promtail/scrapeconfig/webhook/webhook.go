package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/discovery"
	"github.com/prometheus/prometheus/discovery/refresh"
	"github.com/prometheus/prometheus/discovery/targetgroup"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

var (
	webhookSDLookupsCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "wcs_logagent",
			Name:      "sd_webhook_lookups_total",
			Help:      "The number of webhook sd lookups.",
		})
	webhookSDLookupFailuresCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "wcs_logagent",
			Name:      "sd_webhook_lookup_failures_total",
			Help:      "The number of webhook sd lookup failures.",
		})
)

func init() {
	discovery.RegisterConfig(&WebhookSDConfig{})
	fmt.Println("webhook sd config registered")
	prometheus.MustRegister(webhookSDLookupFailuresCount, webhookSDLookupsCount)
}

var DefaultSDConfig = WebhookSDConfig{
	RefreshInterval: model.Duration(30 * time.Second),
}

const (
	subsystemLabel   = model.MetaLabelPrefix + "subsystem"
	subsystemIdLabel = model.MetaLabelPrefix + "subsystemId"
	logDirLabel      = model.MetaLabelPrefix + "logDir"
	HostLabel        = model.MetaLabelPrefix + "host"
)

type WebhookSDConfig struct {
	Address         string         `yaml:"address"`
	RefreshInterval model.Duration `yaml:"refresh_interval,omitempty"`
}

func (*WebhookSDConfig) Name() string { return "webhook" }

// NewDiscoverer returns a Discoverer for the Config.
func (c *WebhookSDConfig) NewDiscoverer(opts discovery.DiscovererOptions) (discovery.Discoverer, error) {
	return NewDiscovery(*c, opts.Logger), nil
}

func (c *WebhookSDConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*c = DefaultSDConfig
	type plain WebhookSDConfig
	err := unmarshal((*plain)(c))
	if err != nil {
		return err
	}
	if c.Address == "" {
		return errors.New("webhook sd config must contain address")
	}
	return nil
}

type Discovery struct {
	*refresh.Discovery
	logger  log.Logger
	Address string
	LocalIP string
}

func NewDiscovery(conf WebhookSDConfig, logger log.Logger) discovery.Discoverer {
	addrs, _ := net.InterfaceAddrs()
	ip := "unknownIP"
	for _, address := range addrs {
		// 检查ip地址判断是否回环地址
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ip = ipnet.IP.String()
			}
		}
	}
	d := Discovery{
		Address: conf.Address,
		LocalIP: ip,
		logger:  logger,
	}
	d.Discovery = refresh.NewDiscovery(
		logger,
		"dns",
		time.Duration(conf.RefreshInterval),
		d.refresh,
	)
	return d
}

func (d *Discovery) refresh(ctx context.Context) ([]*targetgroup.Group, error) {
	webhookSDLookupsCount.Inc()
	response, err := d.callWebhookSD()
	if err != nil {
		level.Warn(d.logger).Log("msg", "discovery vis webhook error", "err", err.Error())
		webhookSDLookupFailuresCount.Inc()
		return nil, err
	}
	// parse response to targetgroups
	tg := d.parseTargets(response)
	return []*targetgroup.Group{tg}, nil
}

func (d *Discovery) parseTargets(resp *WebhookSDResponse) *targetgroup.Group {
	tg := &targetgroup.Group{}
	tg.Source = fmt.Sprintf("%s:%s", d.Address, d.LocalIP)
	for _, si := range resp.SubsystemInfos {
		ls := make(model.LabelSet)
		ls[subsystemLabel] = model.LabelValue(si.SubsystemName)
		ls[subsystemIdLabel] = model.LabelValue(si.SubsystemId)
		if si.LogDir != "" {
			ls[logDirLabel] = model.LabelValue(si.LogDir)
		} else {
			dir := fmt.Sprintf("%s/**", filepath.Join(resp.DefaultLogDir, si.SubsystemName))
			if resp.DefaultLogDir == "" {
				dir = fmt.Sprintf("%s/**", filepath.Join("/data/logs/", si.SubsystemName))
			}
			ls[logDirLabel] = model.LabelValue(dir)
		}
		ls[HostLabel] = model.LabelValue(d.LocalIP)
		labelPrefix := "__label_"
		for k, v := range si.Labels {
			ls[model.LabelName(labelPrefix+k)] = model.LabelValue(v)
		}
		tg.Targets = append(tg.Targets, ls)
	}
	return tg
}

func (d *Discovery) callWebhookSD() (*WebhookSDResponse, error) {
	url := fmt.Sprintf("%s?ip=%s", d.Address, d.LocalIP)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var webhookSDResponse WebhookSDResponse
	err = json.NewDecoder(resp.Body).Decode(&webhookSDResponse)
	if err != nil {
		return nil, err
	}

	return &webhookSDResponse, nil
}

type WebhookSDResponse struct {
	IDC            string          `json:"idc"`
	DCN            string          `json:"dcn"`
	DefaultLogDir  string          `json:"defaultLogDir"`
	SubsystemInfos []SubsystemInfo `json:"subsystemInfos"`
}

type SubsystemInfo struct {
	SubsystemId   string            `json:"subsystemId"`
	SubsystemName string            `json:"subsystemName"`
	LogDir        string            `json:"logDir,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}
