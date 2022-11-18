package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"go.uber.org/atomic"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type AccessServerResponse struct {
	WemqAccessServer string `json:"wemqAccessLogServer"`
}

type ServerList struct {
	WemqAccessServer string `json:"wemqAccessServer"`
}

var serverInfoPattern = "(?P<ip>.*?):(?P<port>.*?)#(?P<weight>.*?)\\|(?P<idc>.*)"

type AccessPicker struct {
	logger log.Logger
	sync.Mutex
	CCEndpoint []string
	CCUri      string
	//CCAddress          flagext.URLValue
	idc                string
	accessEndpointList []string
	idx                *atomic.Int32
	pattern            *regexp.Regexp
}

func NewAccessPicker(ccEndpoint, ccUri, idc string, logger log.Logger) *AccessPicker {
	rand.Seed(time.Now().Unix())
	ri := rand.Int31() % 100
	ap := &AccessPicker{
		logger:             log.With(logger, "component", "AccessPicker", "ccAddress", ccEndpoint),
		CCEndpoint:         strings.Split(ccEndpoint, ";"),
		CCUri:              ccUri,
		idc:                idc,
		accessEndpointList: make([]string, 0),
		idx:                atomic.NewInt32(ri),
		pattern:            regexp.MustCompile(serverInfoPattern),
	}
	go ap.syncIp()
	level.Info(logger).Log("component", "AccessPicker", "status", "new instance successfully", "cc-endpoints", ccEndpoint, "idc", idc, "startIndex", ri)
	return ap
}

func (ap *AccessPicker) Pick() string {
	if len(ap.accessEndpointList) == 0 {
		return ""
	}
	next := ap.idx.Add(1) % int32(len(ap.accessEndpointList))
	ap.idx.Store(next)
	ep := ap.accessEndpointList[next]
	level.Info(ap.logger).Log("function", "Pick", "result", ep)
	return ep
}

func (ap *AccessPicker) syncIp() {
	tick := time.NewTicker(time.Second * 30)
	for {
		select {
		case <-tick.C:
			ap.syncAccessEpOnce()
		}
	}
}

func (ap *AccessPicker) getCCAddress(idx int) string {
	if idx >= len(ap.CCEndpoint) {
		idx = idx % len(ap.CCEndpoint)
	}
	return fmt.Sprintf("%s/%s", ap.CCEndpoint[idx], strings.TrimLeft(ap.CCUri, "/"))
}

func (ap *AccessPicker) syncAccessEpOnce() {
	httpClient := &http.Client{}
	var response *http.Response
	var err error
	for n := range ap.CCEndpoint {
		ccAddress := ap.getCCAddress(n)
		level.Info(ap.logger).Log("ccAddress", ccAddress)
		response, err = httpClient.Get(ccAddress)
		if err == nil {
			break
		}
		level.Warn(ap.logger).Log("getAccessServerResult", "failed", "ccAddress", ccAddress, "response", response)
	}
	if err != nil {
		level.Warn(ap.logger).Log("getAccessServerResult", "failed", "reason", "all endpoint unavailable, sync access endpoint failed")
		return
	}

	buf := bytes.NewBuffer(make([]byte, 0))
	if _, err := io.Copy(buf, response.Body); err != nil {
		level.Warn(ap.logger).Log("operation", "copyData", "err", err.Error())
		return
	}
	acr := &AccessServerResponse{}
	if err := json.Unmarshal(buf.Bytes(), &acr); err != nil {
		level.Warn(ap.logger).Log("operation", "unmarshal AccessServerResponse", "err", err.Error())
		return
	}

	sl := &ServerList{}
	if err := json.Unmarshal([]byte(acr.WemqAccessServer), &sl); err != nil {
		level.Warn(ap.logger).Log("operation", "unmarshal ServerList", "err", err.Error())
		return
	}

	endpointList := make([]string, 0)
	serverInfoStrs := strings.Split(sl.WemqAccessServer, ";")
	for _, s := range serverInfoStrs {
		ip, port, idc := ap.parseAccessServerInfo(s)
		if ip == "" || port == "" || idc == "" {
			continue
		}
		if ap.idc != "" {
			level.Info(ap.logger).Log("function", "syncAccessEpOnce", "needFilterIdc", ap.idc)
			if idc == ap.idc {
				endpointList = append(endpointList, fmt.Sprintf("%s:%s", ip, port))
			}
		} else {
			endpointList = append(endpointList, fmt.Sprintf("%s:%s", ip, port))
		}

	}
	ap.Lock()
	defer ap.Unlock()
	level.Info(ap.logger).Log("function", "syncAccessEpOnce", "result", strings.Join(endpointList, ","))
	ap.accessEndpointList = endpointList
}

func (ap *AccessPicker) parseAccessServerInfo(s string) (ip, port, idc string) {
	matches := ap.pattern.FindStringSubmatch(s)
	if len(matches) < 5 {
		return
	}
	ip, port, idc = matches[1], matches[2], matches[4]
	return
}
