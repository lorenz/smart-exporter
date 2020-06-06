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

	"git.dolansoft.org/dolansoft/smart-exporter/smart"
	"github.com/dswarbrick/smart/drivedb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	smartValue             = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ata_smart_value", Help: "ATA SMART normalized value"}, []string{"dev", "serial", "model", "family", "attr_id", "attr_name"})
	smartRawValue          = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ata_smart_raw_value", Help: "ATA SMART raw decoded value"}, []string{"dev", "serial", "model", "family", "attr_id", "attr_name"})
	collectorDurationValue = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "ata_smart_collector_duration_seconds", Buckets: prometheus.ExponentialBuckets(0.01, 2.0, 10)})
)

var (
	sdRegex = regexp.MustCompile("^sd[a-z]+$")
)

var (
	listenAddr = flag.String("listen-addr", ":9541", "Address the SMART exporter should listen on")
)

func main() {
	prometheus.MustRegister(smartValue)
	prometheus.MustRegister(smartRawValue)
	prometheus.MustRegister(collectorDurationValue)
	flag.Parse()
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(*listenAddr, nil)
	db, err := drivedb.OpenDriveDb("drivedb.yaml")
	if err != nil {
		panic(err)
	}
	for {
		devs, err := ioutil.ReadDir("/dev")
		if err != nil {
			log.Printf("Failed to list devices: %v", err)
			continue
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
				if id.Word85&1 == 1 {
					pages, err := smart.SmartReadData(diskHandle)
					if err != nil {
						log.Printf("Failed to get SMART data: %v", err)
						return
					}
					model := db.LookupDrive(id.ModelNumber())
					for _, attr := range pages.Attrs {
						attrIdStr := strconv.Itoa(int(attr.Id))
						conv, ok := model.Presets[attrIdStr]
						if ok {
							metricValue := attr.RawValue(conv.Conv)
							if metricValue >= 0.0 {
								smartRawValue.WithLabelValues(disk, strings.TrimSpace(string(id.SerialNumber())), strings.TrimSpace(string(id.ModelNumber())), model.Family, attrIdStr, conv.Name).Set(metricValue)
							}
						}
						smartValue.WithLabelValues(disk, strings.TrimSpace(string(id.SerialNumber())), strings.TrimSpace(string(id.ModelNumber())), model.Family, attrIdStr, conv.Name).Set(float64(attr.Value))
					}
				}
			}(disk)
		}
		wg.Wait()
		time.Sleep(1 * time.Minute)
	}
}
