package main

import (
	"flag"
	"fmt"
	"github.com/mrunalp/fileutils"
	"os"
	"reflect"
	"runtime"
	"sync"

	// embed time zone data
	_ "time/tzdata"

	"k8s.io/klog"

	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/flagext"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/version"
	"github.com/weaveworks/common/logging"
	"github.com/weaveworks/common/tracing"

	"github.com/grafana/loki/clients/pkg/logentry/stages"
	"github.com/grafana/loki/clients/pkg/promtail"
	"github.com/grafana/loki/clients/pkg/promtail/client"
	promtail_config "github.com/grafana/loki/clients/pkg/promtail/config"

	"github.com/grafana/loki/pkg/util"
	"github.com/grafana/loki/pkg/util/cfg"

	_ "github.com/grafana/loki/pkg/util/build"
	util_log "github.com/grafana/loki/pkg/util/log"
)

func init() {
	prometheus.MustRegister(version.NewCollector("promtail"))
}

var mtx sync.Mutex

type Config struct {
	promtail_config.Config `yaml:",inline"`
	printVersion           bool
	enabledCfgInjecting    bool
	printConfig            bool
	logConfig              bool
	dryRun                 bool
	checkSyntax            bool
	configFile             string
	configExpandEnv        bool
	stdLogging             bool
	logDir                 string
	inspect                bool
}

func (c *Config) RegisterFlags(f *flag.FlagSet) {
	f.BoolVar(&c.printVersion, "version", false, "Print this builds version information")
	f.BoolVar(&c.enabledCfgInjecting, "enable-cfg-injecting", false, "enable config injecting")
	f.BoolVar(&c.printConfig, "print-config-stderr", false, "Dump the entire Loki config object to stderr")
	f.BoolVar(&c.logConfig, "log-config-reverse-order", false, "Dump the entire Loki config object at Info log "+
		"level with the order reversed, reversing the order makes viewing the entries easier in Grafana.")
	f.BoolVar(&c.dryRun, "dry-run", false, "Start Promtail but print entries instead of sending them to Loki.")
	f.BoolVar(&c.checkSyntax, "check-syntax", false, "Validate the config file of its syntax")
	f.BoolVar(&c.inspect, "inspect", false, "Allows for detailed inspection of pipeline stages")
	f.StringVar(&c.configFile, "config.file", "", "yaml file to load")
	f.BoolVar(&c.stdLogging, "std-logging", false, "output log to stdout")
	f.StringVar(&c.logDir, "log-dir", "", "dir to storing logs")
	f.BoolVar(&c.configExpandEnv, "config.expand-env", false, "Expands ${var} in config according to the values of the environment variables.")
	c.Config.RegisterFlags(f)
}

// Clone takes advantage of pass-by-value semantics to return a distinct *Config.
// This is primarily used to parse a different flag set without mutating the original *Config.
func (c *Config) Clone() flagext.Registerer {
	return func(c Config) *Config {
		return &c
	}(*c)
}

// wrap os.Exit so that deferred functions execute before the process exits
func exit(code int) {
	// flush all logs that may be buffered in memory
	util_log.Flush()

	os.Exit(code)
}

func main() {
	runtime.GOMAXPROCS(1)
	// Load config, merging config file and CLI flags
	var config Config
	args := os.Args[1:]
	if err := cfg.DefaultUnmarshal(&config, args, flag.CommandLine); err != nil {
		fmt.Println("Unable to parse config:", err)
		exit(1)
	}
	if config.checkSyntax {
		if config.configFile == "" {
			fmt.Println("Invalid config file")
			exit(1)
		}
		fmt.Println("Valid config file! No syntax issues found")
		exit(0)
	}

	// Handle -version CLI flag
	if config.printVersion {
		fmt.Println(version.Print("promtail"))
		exit(0)
	}

	// Init the logger which will honor the log level set in cfg.Server
	if reflect.DeepEqual(&config.Config.ServerConfig.Config.LogLevel, &logging.Level{}) {
		fmt.Println("Invalid log level")
		exit(1)
	}
	util_log.InitLogger(&config.Config.ServerConfig.Config, prometheus.DefaultRegisterer, true, false, config.stdLogging, config.logDir)

	// Use Stderr instead of files for the klog.
	klog.SetOutput(os.Stderr)

	if config.inspect {
		stages.Inspect = true
	}

	// Set the global debug variable in the stages package which is used to conditionally log
	// debug messages which otherwise cause huge allocations processing log lines for log messages never printed
	if config.Config.ServerConfig.Config.LogLevel.String() == "debug" {
		stages.Debug = true
	}

	if config.printConfig {
		err := util.PrintConfig(os.Stderr, &config)
		if err != nil {
			level.Error(util_log.Logger).Log("msg", "failed to print config to stderr", "err", err.Error())
		}
	}

	if config.logConfig {
		err := util.LogConfig(&config)
		if err != nil {
			level.Error(util_log.Logger).Log("msg", "failed to log config object", "err", err.Error())
		}
	}

	if config.Tracing.Enabled {
		// Setting the environment variable JAEGER_AGENT_HOST enables tracing
		trace, err := tracing.NewFromEnv("promtail")
		if err != nil {
			level.Error(util_log.Logger).Log("msg", "error in initializing tracing. tracing will not be enabled", "err", err)
		}

		defer func() {
			if trace != nil {
				if err := trace.Close(); err != nil {
					level.Error(util_log.Logger).Log("msg", "error closing tracing", "err", err)
				}
			}
		}()
	}

	clientMetrics := client.NewMetrics(prometheus.DefaultRegisterer, config.Config.Options.StreamLagLabels)
	newConfigFunc := func() (*promtail_config.Config, error) {
		mtx.Lock()
		defer mtx.Unlock()
		var config Config
		if err := cfg.DefaultUnmarshal(&config, args, flag.NewFlagSet(os.Args[0], flag.ExitOnError)); err != nil {
			fmt.Println("Unable to parse config:", err)
			return nil, fmt.Errorf("unable to parse config: %w", err)
		}
		return &config.Config, nil
	}

	var injectConfigFunc func(content string) error = nil
	if config.enabledCfgInjecting {
		injectConfigFunc = func(content string) error {
			mtx.Lock()
			defer mtx.Unlock()
			var config Config
			if err := cfg.DefaultUnmarshal(&config, args, flag.NewFlagSet(os.Args[0], flag.ExitOnError)); err != nil {
				fmt.Println("Unable to parse config:", err)
				return fmt.Errorf("unable to parse config: %w", err)
			}
			if err := fileutils.CopyFile(config.configFile, "/tmp/config-backup.yaml"); err != nil {
				return fmt.Errorf("backup old config failed when injecting config, err=%s", err.Error())
			}
			defer func() {
				err := os.Remove("/tmp/config-backup.yaml")
				if err != nil {
					level.Warn(util_log.Logger).Log("msg", "injectConfig: clean backup config", "err", err.Error())
				}

			}()
			rollbackFunc := func() {
				err := fileutils.CopyFile("/tmp/config-backup.yaml", config.configFile)
				if err != nil {
					level.Warn(util_log.Logger).Log("msg", "injectConfig: rollback config error", "err", err.Error())
				}
			}

			f, err := os.Create(config.configFile)
			if err != nil {
				rollbackFunc()
				return fmt.Errorf("write new config failed when openning file, err=%s", err.Error())
			}

			if _, err := f.WriteString(content); err != nil {
				rollbackFunc()
				return fmt.Errorf("write file failed, err=%s", err.Error())
			}
			f.Close()
			var tmp = new(Config)

			if err := cfg.YAML(config.configFile, config.configExpandEnv, true)(tmp); err != nil {
				rollbackFunc()
				return fmt.Errorf("try load config failed, err=%s", err.Error())
			}
			return nil
		}
	}

	p, err := promtail.New(config.Config, newConfigFunc, injectConfigFunc, clientMetrics, config.dryRun)
	if err != nil {
		level.Error(util_log.Logger).Log("msg", "error creating promtail", "error", err)
		exit(1)
	}

	level.Info(util_log.Logger).Log("msg", "Starting Promtail", "version", version.Info())
	defer p.Shutdown()

	if err := p.Run(); err != nil {
		level.Error(util_log.Logger).Log("msg", "error starting promtail", "error", err)
		exit(1)
	}
}
