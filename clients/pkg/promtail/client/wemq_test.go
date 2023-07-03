package client

import (
	"fmt"
	util_log "github.com/GoneLikeAir/loki/pkg/util/log"
	"github.com/grafana/dskit/flagext"
	"go.uber.org/atomic"
	"net/url"
	"sync"
	"testing"
)

func TestAccessPicker(t *testing.T) {

	ccAddress := flagext.URLValue{
		URL: &url.URL{Host: "10.107.117.12:8090",
			Scheme: "http",
			Path:   "/dynamicKey/v1/wemqAccessLogServer.json",
		},
	}
	picker := NewAccessPicker(ccAddress, "D", util_log.Logger)

	picker.accessEndpointList = []string{"127.0.0.1:8888", "127.0.0.2:8888", "127.0.0.3:8888", "127.0.0.4:8888"}

	ip1Count := atomic.NewInt32(0)
	ip2Count := atomic.NewInt32(0)
	ip3Count := atomic.NewInt32(0)
	ip4Count := atomic.NewInt32(0)
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 1; i <= 100; i++ {
			ep := picker.Pick()
			switch ep {
			case picker.accessEndpointList[0]:
				ip1Count.Add(1)
			case picker.accessEndpointList[1]:
				ip2Count.Add(1)
			case picker.accessEndpointList[2]:
				ip3Count.Add(1)
			case picker.accessEndpointList[3]:
				ip4Count.Add(1)
			}
			// fmt.Println("loop1", ep)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 1; i <= 100; i++ {
			ep := picker.Pick()
			switch ep {
			case picker.accessEndpointList[0]:
				ip1Count.Add(1)
			case picker.accessEndpointList[1]:
				ip2Count.Add(1)
			case picker.accessEndpointList[2]:
				ip3Count.Add(1)
			case picker.accessEndpointList[3]:
				ip4Count.Add(1)
			}
			// fmt.Println("loop2", ep)
		}
	}()

	wg.Wait()

	fmt.Println(ip1Count.Load(), ip2Count.Load(), ip3Count.Load(), ip4Count.Load())

}
