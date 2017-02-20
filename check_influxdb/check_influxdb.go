package main

import (
	"encoding/json"
	"fmt"
	"github.com/ajgb/go-plugin"
	"github.com/influxdata/influxdb/client/v2"
	"github.com/influxdata/influxdb/models"
	"os"
	"strings"
)

var opts struct {
	Hostname           string            `short:"H" long:"hostname" description:"InfluxDB server" default:"http://localhost:8086" required:"true"`
	Username           string            `short:"u" long:"username" description:"Username"`
	Password           string            `short:"p" long:"password" description:"Password"`
	RunMode            string            `short:"r" long:"run" description:"Run mode: stats or query" default:"stats" required:"true"`
	Module             string            `short:"M" long:"module" description:"Stats module" default:"runtime" required:"true"`
	Tags               map[string]string `short:"t" long:"tag" description:"Additional key:value tags identifying stats module"`
	Metrics            []string          `short:"m" long:"metric" description:"Filtered list of metrics"`
	Query              string            `short:"q" long:"query" description:"Query to execute in query mode"`
	WarningThreshold   string            `short:"w" long:"warning" description:"Warning threshold"`
	CriticalThreshold  string            `short:"c" long:"critical" description:"Critical threshold"`
	InsecureSkipVerify bool              `long:"ignore-ssl-errors" description:"Ignore SSL certificate errors"`
	UOM                string            `long:"uom" description:"UOM for metrics"`
}

func main() {
	var (
		mode_query string
		database   string
		results    []client.Result
		got_data   bool
	)
	wanted_metrics := make(map[string]bool)

	check := plugin.New("check_influxdb", "1.0.0")
	check.Preamble = `Copyright (c) 2017 Alex J. G. Burzyński (ajgb@cpan.org)

This plugin tests InfluxDB TimeSeries database server.
`

	check.Description = `DESCRIPTION

Plugin supports following run modes:
- stats:    runs SHOW STATS FOR 'MODULE'.
            Where MODULE is provided by [-M|--module] parameter.
            If given module returns multiple series, use [-t|--tag] to locate
            requested data.
            List of returned metrics could be limited by providing [-m|--metric]
            parameters.

- query:    executes specified query on _internal database.
            Multiple queries (separated by semicolon) could be executed as long as
            each returns only one row and no duplicated metrics are to be found.

Note: Warning and critical thresholds are applied to all metrics.

Examples:
- List only specified metrics from runtime
$ check_influxdb -m Alloc -m TotalAlloc --uom c
OK: runtime Stats for: Alloc, TotalAlloc | Alloc=24334856c;;;; TotalAlloc=13365442832c;;;;

- Alert if there were write errors in last 5 minutes
$ check_influxdb -r query -w 1 -c 5 -q='SELECT DIFFERENCE(MAX(writeError)) AS writeErrors FROM "write" WHERE time > now() - 5m GROUP BY time(5m) LIMIT 1 OFFSET 1'
WARNING: Query 'SELECT DIFFERENCE(MAX(writeError)) AS writeErrors FROM "write" WHERE time > now() - 5m GROUP BY time(5m) LIMIT 1 OFFSET 1', writeErrors is 2 (outside 1) | writeErrors=2;1;5;;

- Alert if number of series is growing out of control
$ check_influxdb -M database -t database:measurements -m numSeries -w 1000 -c 10000
OK: database stats (database:measurements) for: numSeries | numSeries=896;1000;10000;;

- Check disk usage for given database shard by its id 
$ check_influxdb -M shard -t database:measurements -t id:20 -m diskBytes
OK: shard stats (database:measurements, id:20) for: diskBytes | diskBytes=972026;;;;
`

	if err := check.ParseArgs(&opts); err != nil {
		check.ExitCritical("Error parsing arguments: %s\n", err)
	}
	defer check.Final()

	// mode specific settings
	switch opts.RunMode {
	case "stats":
		mode_query = fmt.Sprintf("SHOW STATS FOR '%s'", opts.Module)

		for _, m := range opts.Metrics {
			wanted_metrics[m] = true
		}
		msg := fmt.Sprintf("%s stats", opts.Module)
		if len(opts.Tags) > 0 {
			tags := make([]string, 0, len(opts.Tags))
			for k, v := range opts.Tags {
				tags = append(tags, fmt.Sprintf("%s:%s", k, v))
			}
			msg += "(" + strings.Join(tags, ", ") + ")"
		}
		if len(opts.Metrics) > 0 {
			msg += " for: " + strings.Join(opts.Metrics, ", ")
		}
		check.AddMessage(msg)
	case "query":
		if len(opts.Query) > 0 {
			mode_query = opts.Query
		} else {
			check.ExitCritical("Query parameter required in query mode\n")
		}
		check.AddMessage("Query '%s'", opts.Query)
		database = "_internal"
	default:
		check.ExitCritical("Unknown mode: %s\n", opts.RunMode)
	}

	// Influxdb Client
	clientConfig := client.HTTPConfig{
		Addr:               opts.Hostname,
		InsecureSkipVerify: opts.InsecureSkipVerify,
	}

	if len(opts.Username) > 0 {
		clientConfig.Username = opts.Username
		clientConfig.Password = opts.Password
	}
	db, err := client.NewHTTPClient(clientConfig)
	if err != nil {
		check.ExitCritical("Failed to create InfluxDB client: %s", err)
	}
	defer db.Close()

	// execute query
	q := client.Query{
		Command:   mode_query,
		Database:  database,
		Precision: "s",
	}
	if response, err := db.Query(q); err == nil {
		if resError := response.Error(); resError != nil {
			check.ExitCritical("Request error: %s", resError)
		}
		results = response.Results
	} else {
		check.ExitCritical("Failed to query InfluxDB server: %s", err)
	}

	// process response
	for _, r := range results {
		for _, s := range r.Series {
			if seriesMatched(s) {
				// multiple rows would mean duplicated values for metrics
				if len(s.Values) > 1 {
					check.ExitCritical("Query returns multiple rows")
				}
				if len(s.Values) != 1 {
					continue
				}
				for i, n := range s.Columns {
					// skip time column returned in Query mode
					if opts.RunMode == "query" && n == "time" {
						continue
					}
					// accept all columns returned in Query mode
					// or if metric  was requested
					// or no filter was specified
					if _, ok := wanted_metrics[n]; opts.RunMode == "query" || ok || len(wanted_metrics) == 0 {
						v, _ := s.Values[0][i].(json.Number).Int64()
						err := check.AddMetric(n, v, opts.UOM, opts.WarningThreshold, opts.CriticalThreshold)
						if err != nil {
							check.ExitCritical("%s", err)
						}
						got_data = true
					}
				}
			}
		}
	}

	if !got_data {
		check.ExitCritical("No data returned for %s", os.Args[1:])
	}
}

func seriesMatched(series models.Row) bool {
	tags_provided := len(opts.Tags)
	if opts.RunMode == "query" || len(opts.Module) == 0 {
		return true
	}

	if series.Name == opts.Module {
		if tags_provided == 0 {
			return true
		}

		tags_matched := 0
		for k, expected := range opts.Tags {
			if got, ok := series.Tags[k]; ok && got == expected {
				tags_matched++
			}
		}
		if tags_provided == tags_matched {
			return true
		}
	}

	return false
}
