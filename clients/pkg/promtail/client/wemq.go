package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/flagext"
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
	CCAddress          flagext.URLValue
	idc                string
	accessEndpointList []string
	idx                *atomic.Int32
	pattern            *regexp.Regexp
}

func NewAccessPicker(ccAddress flagext.URLValue, idc string, logger log.Logger) *AccessPicker {
	rand.Seed(time.Now().Unix())
	ri := rand.Int31() % 100
	ap := &AccessPicker{
		logger:             log.With(logger, "component", "AccessPicker", "ccAddress", ccAddress),
		CCAddress:          ccAddress,
		idc:                idc,
		accessEndpointList: make([]string, 0),
		idx:                atomic.NewInt32(ri),
		pattern:            regexp.MustCompile(serverInfoPattern),
	}
	go ap.syncIp()
	level.Info(logger).Log("component", "AccessPicker", "status", "new instance successfully", "cc-address", ccAddress, "idc", idc, "startIndex", ri)
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
			ap.syncIpOnce()
		}
	}
}

func (ap *AccessPicker) syncIpOnce() {
	httpClient := &http.Client{}
	response, err := httpClient.Get(ap.CCAddress.String())
	if err != nil {
		level.Warn(ap.logger).Log("getAccessServerResult", "failed", "response", response)
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
			if idc == ap.idc {
				endpointList = append(endpointList, fmt.Sprintf("%s:%s", ip, port))
			}
		} else {
			endpointList = append(endpointList, fmt.Sprintf("%s:%s", ip, port))
		}

	}
	ap.Lock()
	defer ap.Unlock()
	level.Info(ap.logger).Log("function", "syncIpOnce", "result", endpointList)
	ap.accessEndpointList = endpointList
}

func (ap *AccessPicker) parseAccessServerInfo(s string) (ip, port, idc string) {
	matches := ap.pattern.FindStringSubmatch(s)
	ip = ""
	port = ""
	idc = ""
	for i, m := range matches {
		if i == 1 {
			ip = m
		}
		if i == 2 {
			port = m
		}
		if i == 4 {
			idc = m
		}
	}
	return
}
