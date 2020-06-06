package smart

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/yalue/native_endian"
)

const (
	// ATA commands
	ATA_SMART = 0xb0

	// ATA feature register values for SMART
	SMART_READ_DATA     = 0xd0
	SMART_READ_LOG      = 0xd5
	SMART_RETURN_STATUS = 0xda
)

// SmartReadData issues a SMART_READ_DATA command to the open disk
func SmartReadData(dev *os.File) (SmartPage, error) {
	cdb := CDB16{SCSI_ATA_PASSTHRU_16}
	cdb[1] = 0x08            // ATA protocol (4 << 1, PIO data-in)
	cdb[2] = 0x0e            // BYT_BLOK = 1, T_LENGTH = 2, T_DIR = 1
	cdb[4] = SMART_READ_DATA // feature LSB
	cdb[10] = 0x4f           // low lba_mid
	cdb[12] = 0xc2           // low lba_high
	cdb[14] = ATA_SMART      // command

	respBuf := make([]byte, 512)
	smart := SmartPage{}

	if err := SendCDB(dev, cdb[:], &respBuf); err != nil {
		return smart, fmt.Errorf("SMART READ DATA: %v", err)
	}

	err := binary.Read(bytes.NewBuffer(respBuf[:362]), native_endian.NativeEndian(), &smart)
	return smart, err
}

// Individual SMART attribute (12 bytes)
type SmartAttr struct {
	Id          uint8
	Flags       uint16
	Value       uint8   // normalised value
	Worst       uint8   // worst value
	VendorBytes [6]byte // vendor-specific (and sometimes device-specific) data
	Reserved    uint8
}

// Page of 30 SMART attributes as per ATA spec
type SmartPage struct {
	Version uint16
	Attrs   [30]SmartAttr
}

// SMART log address 00h
type SmartLogDirectory struct {
	Version uint16
	Address [255]struct {
		NumPages byte
		_        byte // Reserved
	}
}

// SMART log address 01h
type SmartSummaryErrorLog struct {
	Version    byte
	LogIndex   byte
	LogData    [5][90]byte // TODO: Expand out to error log structure
	ErrorCount uint16      // Device error count
	_          [57]byte    // Reserved
	Checksum   byte        // Two's complement checksum of first 511 bytes
}

// SMART log address 06h
type SmartSelfTestLog struct {
	Version uint16
	Entry   [21]struct {
		LBA_7          byte   // Content of the LBA field (7:0) when subcommand was issued
		Status         byte   // Self-test execution status
		LifeTimestamp  uint16 // Power-on lifetime of the device in hours when subcommand was completed
		Checkpoint     byte
		LBA            uint32 // LBA of first error (28-bit addressing)
		VendorSpecific [15]byte
	}
	VendorSpecific uint16
	Index          byte
	_              uint16 // Reserved
	Checksum       byte   // Two's complement checksum of first 511 bytes
}

// DecodeVendorBytes decodes the six-byte vendor byte array based on the conversion rule passed as
// conv. The conversion may also include the reserved byte, normalised value or worst value byte.
func (sa *SmartAttr) DecodeVendorBytes(conv string) uint64 {
	var (
		byteOrder string
		r         uint64
	)

	// Default byte orders if not otherwise specified in drivedb
	switch conv {
	case "raw64", "hex64":
		byteOrder = "543210wv"
	case "raw56", "hex56", "raw24/raw32", "msec24hour32":
		byteOrder = "r543210"
	default:
		byteOrder = "543210"
	}

	// Pick bytes from smartAttr in order specified by byteOrder
	for _, i := range byteOrder {
		var b byte

		switch i {
		case '0', '1', '2', '3', '4', '5':
			b = sa.VendorBytes[i-48]
		case 'r':
			b = sa.Reserved
		case 'v':
			b = sa.Value
		case 'w':
			b = sa.Worst
		default:
			b = 0
		}

		r <<= 8
		r |= uint64(b)
	}

	return r
}

func checkTempRange(t int8, ut1, ut2 uint8, lo, hi *int) bool {
	t1, t2 := int8(ut1), int8(ut2)

	if t1 > t2 {
		t1, t2 = t2, t1
	}

	if (-60 <= t1) && (t1 <= t) && (t <= t2) && (t2 <= 120) && !(t1 == -1 && t2 <= 0) {
		*lo, *hi = int(t1), int(t2)
		return true
	}

	return false
}

func checkTempWord(word uint16) int {
	switch {
	case word <= 0x7f:
		return 0x11 // >= 0, signed byte or word
	case word <= 0xff:
		return 0x01 // < 0, signed byte
	case word > 0xff80:
		return 0x10 // < 0, signed word
	default:
		return 0x00
	}
}

// Value makes an attempt to convert the raw value to a single value according to the conversion rule
// specified. The conversion rule usually comes from drivedb.
func (sa *SmartAttr) RawValue(conv string) float64 {
	var (
		raw  [6]uint8
		word [3]uint16
	)
	v := sa.DecodeVendorBytes(conv)

	// Split into bytes
	for i := 0; i < 6; i++ {
		raw[i] = uint8(v >> uint(i*8))
	}

	// Split into words
	for i := 0; i < 3; i++ {
		word[i] = uint16(v >> uint(i*16))
	}

	switch conv {
	case "raw8":
		return -1.0
	case "raw16":
		return float64(word[2])
	case "raw48", "raw56", "raw64", "hex48", "hex56", "hex64":
		return float64(v)
	case "raw16(raw16)", "raw16(avg16)":
		return float64(word[0])
	case "raw24(raw8)":
		return float64(v & 0x00ffffff)
	case "raw24/raw24":
		return float64(v >> 24)
	case "raw24/raw32":
		return float64(v >> 32)
	case "min2hour":
		return float64(uint64(word[0])+uint64(word[1])<<16) / 60.0
	case "sec2hour":
		return float64(v) / 3600.0
	case "halfmin2hour":
		return float64(v) / 120.0
	case "msec24hour32":
		// hours + milliseconds
		hours := v & 0xffffffff
		milliseconds := v >> 32
		return float64(hours) + float64(milliseconds)/3600000.0
	case "tempminmax":
		var tFormat, lo, hi int

		t := int8(raw[0])
		ctw0 := checkTempWord(word[0])

		if word[2] == 0 {
			if (word[1] == 0) && (ctw0 != 0) {
				// 00 00 00 00 xx TT
				tFormat = 0
			} else if (ctw0 != 0) && checkTempRange(t, raw[2], raw[3], &lo, &hi) {
				// 00 00 HL LH xx TT
				tFormat = 1
			} else if (raw[3] == 0) && checkTempRange(t, raw[1], raw[2], &lo, &hi) {
				// 00 00 00 HL LH TT
				tFormat = 2
			} else {
				tFormat = -1
			}
		} else if ctw0 != 0 {
			if (ctw0&checkTempWord(word[1])&checkTempWord(word[2]) != 0x00) && checkTempRange(t, raw[2], raw[4], &lo, &hi) {
				// xx HL xx LH xx TT
				tFormat = 3
			} else if (word[2] < 0x7fff) && checkTempRange(t, raw[2], raw[3], &lo, &hi) && (hi >= 40) {
				// CC CC HL LH xx TT
				tFormat = 4
			} else {
				tFormat = -2
			}
		} else {
			tFormat = -3
		}

		switch tFormat {
		case 0:
			return float64(t)
		case 1, 2, 3:
			return float64(t)
		case 4:
			return float64(t)
		default:
			return float64(raw[0])
		}
	case "temp10x":
		return float64(word[0]) / 10.0
	default:
		return -1.0
	}
}
