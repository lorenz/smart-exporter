# SMART exporter
*A pure-Go ATA **S**elf-**M**onitoring, **A**nalysis and **R**eporting **T**echnology data exporter*

:warning: This attempts to send `ATA PASS THROUGH` SCSI commands to all SCSI disks in your system.
These should just fail if there's no ATA disk there, but it cannot be ruled out that this may
adversely affect a SCSI device or controller. Sadly other ways of identifying SATA disks are either
slow or unreliable.

A big thanks goes to Daniel Swarbrick for his work on https://github.com/dswarbrick/smart which this
is in part based on.

# Quick start
```
go build .
./smart-exporter
```

As this is a Go program you can just copy the binary (and `drivedb.yaml`) anywhere and run it.
Metrics are exposed on `your-host:9541/metrics` by default. This can be changed using `--listen-addr`.

# Limitations
* Currently does not export thresholds (would need to send another ATA command for each disk)
* Does not export multi-valued raw data (only the first value is exported)

