package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/ajgb/go-config"
	"github.com/ajgb/go-plugin"
	"net/http"
	"strings"
	"time"
)

var opts struct {
	Hostname           string   `short:"H" long:"hostname" description:"Application host" default:"localhost" required:"true"`
	Schema             string   `short:"s" long:"schema" description:"Protocol schema" default:"http" required:"true"`
	Port               int      `short:"P" long:"port" description:"Application port" default:"8080" required:"true"`
	Username           string   `short:"u" long:"username" description:"Username"`
	Password           string   `short:"p" long:"password" description:"Password"`
	Message            string   `short:"M" long:"message" description:"Initial plugin message"`
	Keys               []string `short:"m" long:"metric" description:"List of path based keys to query" required:"true"`
	BasenameMetric     bool     `short:"b" long:"basename" description:"Ignore leading path of metrics"`
	WarningThreshold   string   `short:"w" long:"warning" description:"Warning threshold"`
	CriticalThreshold  string   `short:"c" long:"critical" description:"Critical threshold"`
	InsecureSkipVerify bool     `long:"ignore-ssl-errors" description:"Ignore SSL certificate errors"`
	UOM                string   `long:"uom" description:"UOM for keys"`
	Timeout            int      `long:"timeout" description:"Connection timeout in seconds" default:"30"`
	Path               string   `short:"U" long:"path" description:"Handler URL path" default:"/" required:"true"`
}

type jsonData map[string]interface{}

func main() {
	// init plugin
	check := checkPlugin()

	if err := check.ParseArgs(&opts); err != nil {
		check.ExitCritical("Error parsing arguments: %s\n", err)
	}
	defer check.Final()

	client := httpClient()

	url := makeUrl()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		check.ExitCritical("Failed to create HTTP request: %s", err)
	}
	req.Header.Add("User-Agent", fmt.Sprintf("%s/%s", check.Name, check.Version))
	if opts.Username != "" {
		req.SetBasicAuth(opts.Username, opts.Password)
	}

	switch len(opts.Message) {
	case 0:
		check.AddMessage(url)
	default:
		check.AddMessage(opts.Message)
	}

	resp, err := client.Do(req)
	if err != nil {
		check.ExitCritical("HTTP request failed: %s", err)
	}
	if resp.StatusCode != 200 {
		check.ExitCritical("HTTP request failed: %s", resp.Status)
	}
	defer resp.Body.Close()

	data, err := config.ProcessJson(resp.Body)
	if err != nil {
		check.ExitCritical("Failed to decode JSON response: %s", err)
	}
	for _, key := range opts.Keys {
		addKey(check, data, key)
	}
}

func addKey(check *plugin.Plugin, data *config.Config, key string) {
	value, err := config.Get(data.Root, key)
	if err != nil {
		check.ExitUnknown("Unable to locate key %s: %s", key, err)
	}

	switch value.(type) {
	case json.Number:
		value, err := data.Number(key)
		if err != nil {
			check.ExitUnknown("Unable to process key %s as number: %s", key, err)
		}
		check.AddMetric(basename(key), value, opts.UOM, opts.WarningThreshold, opts.CriticalThreshold)
	case map[string]interface{}:
		subtree, err := data.Map(key)
		if err != nil {
			check.ExitUnknown("Unable to process key %s as map: %s", key, err)
		}
		for child_key, _ := range subtree {
			addKey(check, data, fmt.Sprintf("%s.%s", key, child_key))
		}
	case []interface{}, []string, []json.Number, []int, []float64:
		// skip slices
	default:
		value, err := data.String(key)
		if err != nil {
			check.ExitUnknown("Unable to process key %s as string: %s", key, err)
		}
		check.AddMessage("%s is %s", basename(key), value)
	}
}

func basename(key string) string {
	if opts.BasenameMetric {
		if i := strings.LastIndex(key, "."); i >= 0 {
			return key[i+1:]
		}
	}
	return key
}

func makeUrl() string {
	return fmt.Sprintf("%s://%s:%d%s", opts.Schema, opts.Hostname, opts.Port, opts.Path)
}

func httpClient() *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: opts.InsecureSkipVerify,
		},
	}
	client := &http.Client{
		Timeout:   time.Duration(opts.Timeout) * time.Second,
		Transport: transport,
	}

	return client
}

func checkPlugin() *plugin.Plugin {
	check := plugin.New("check_json_api", "v1.0.0")
	check.Preamble = `Copyright (c) 2017 Alex J. G. Burzyński (ajgb@ajgb.org)

This plugin tests JSON based API provided by many applications.
`

	check.Description = `DESCRIPTION

Supported key format:
- toplevelmetric - { "toplevelmetric": ... }
- parent.child   - { "parent": { "child": ... } ... }
- list.1.item    - { "list": [ { ... }, { "item": ... } ... ] }

If key path points to object all its children are returned.

List values are ignored.

Numeric items are added to perfomance data, anything else is added to check message.

Note: Warning and critical thresholds are applied to all metrics.

Examples:
- Check expvar metrics for InfluxDB
$ check_api_json -H localhost -P 8086 -U /debug/vars -b -M "Memstats metrics" -m memstats.Alloc -m memstats.GCCPUFraction
OK: Memstats metrics | Alloc=52836064;;;; GCCPUFraction=0.0001307805780720632;;;;

- Check Jenkins test job results
$ check_api_json -H localhost -U /job/PROJECT/api/json -M "Job Summary" -m healthReport.0.description
OK: Job Summary, Test Result: 1,234 tests failing out of a total of 56,789 tests.
`
	return check
}
