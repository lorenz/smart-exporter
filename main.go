package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.dolansoft.org/dolansoft/smart-exporter/drivedb"
	"git.dolansoft.org/dolansoft/smart-exporter/smart"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	collectorDurationValue = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "ata_smart_collector_duration_seconds", Buckets: prometheus.ExponentialBuckets(0.01, 2.0, 10)})
)

var (
	sdRegex = regexp.MustCompile("^sd[a-z]+$")
)

var (
	listenAddr = flag.String("listen-addr", ":9541", "Address the SMART exporter should listen on")
)

type smartCollector struct {
	smartValue    *prometheus.Desc
	smartRawValue *prometheus.Desc
	db            drivedb.DriveDb
}

func newSMARTCollector() *smartCollector {
	db, err := drivedb.OpenDriveDb("drivedb.yaml")
	if err != nil {
		panic(err)
	}
	return &smartCollector{
		smartValue:    prometheus.NewDesc("ata_smart_value", "ATA SMART normalized value", []string{"dev", "serial", "model", "family", "attr_id", "attr_name"}, nil),
		smartRawValue: prometheus.NewDesc("ata_smart_raw_value", "ATA SMART raw decoded value", []string{"dev", "serial", "model", "family", "attr_id", "attr_name"}, nil),
		db:            db,
	}
}

func (c *smartCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.smartValue
	ch <- c.smartRawValue
}

func (c *smartCollector) Collect(ch chan<- prometheus.Metric) {
	devs, err := ioutil.ReadDir("/dev")
	if err != nil {
		log.Printf("Failed to list devices: %v", err)
		return
	}
	var diskDevs []string
	for _, dev := range devs {
		if sdRegex.MatchString(dev.Name()) {
			diskDevs = append(diskDevs, dev.Name())
		}
	}
	wg := sync.WaitGroup{}
	for _, disk := range diskDevs {
		wg.Add(1)
		go func(disk string) {
			startTime := time.Now()
			defer func() {
				duration := time.Now().Sub(startTime)
				collectorDurationValue.Observe(duration.Seconds())
			}()
			defer wg.Done()
			diskHandle, err := os.Open("/dev/" + disk)
			if err != nil {
				log.Printf("Failed to open device %v: %v", disk, err)
				return
			}
			defer diskHandle.Close()
			id, err := smart.Identify(diskHandle)
			if err != nil {
				return
			}
			if id.Word85&1 == 1 { // Disk has SMART
				pages, err := smart.SmartReadData(diskHandle)
				if err != nil {
					log.Printf("Failed to get SMART data: %v", err)
					return
				}
				model := c.db.LookupDrive(id.ModelNumber())
				for _, attr := range pages.Attrs {
					attrIdStr := strconv.Itoa(int(attr.Id))
					if attr.Id == 0 {
						continue
					}
					conv, ok := model.Presets[attrIdStr]
					labels := []string{disk, strings.TrimSpace(string(id.SerialNumber())), strings.TrimSpace(string(id.ModelNumber())), model.Family, attrIdStr, conv.Name}
					if ok {
						metricValue := attr.RawValue(conv.Conv)
						if metricValue >= 0.0 {
							ch <- prometheus.MustNewConstMetric(c.smartRawValue, prometheus.UntypedValue, metricValue, labels...)
						}
					}
					ch <- prometheus.MustNewConstMetric(c.smartValue, prometheus.UntypedValue, float64(attr.Value), labels...)
				}
			}
		}(disk)
	}
	wg.Wait()
}

func main() {
	smartC := newSMARTCollector()
	prometheus.MustRegister(smartC)
	prometheus.MustRegister(collectorDurationValue)
	flag.Parse()
	http.Handle("/metrics", promhttp.Handler())
	if err := http.ListenAndServe(*listenAddr, nil); err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
}
