package can

import "fmt"

type DataFrame struct {
	id   uint16  // 11 bits   [ 0-10]
	dlc  uint8   // 4 bits    [11-14]
	data []uint8 // 0-8 bytes [15-78]
}

func NewDataFrame(id uint16, data ...uint8) (*DataFrame, error) {
	if id >= 1<<11 {
		return nil, fmt.Errorf("id too big")
	}
	if len(data) > 8 {
		return nil, fmt.Errorf("too much data")
	}
	return &DataFrame{
		id:   id,
		dlc:  uint8(len(data)),
		data: data,
	}, nil
}

func (f *DataFrame) Size() int {
	return 11 + 4 + int(8*f.dlc)
}

func (f *DataFrame) Bit(i int) uint8 {
	if i < 11 {
		// ID
		return uint8((f.id >> (10 - i)) & 1)
	}
	if i < 15 {
		// DLC
		return uint8((f.dlc >> (14 - i)) & 1)
	}
	if i < 23 {
		// Data[0]
		if f.dlc < 1 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[0] >> (22 - i)) & 1)
	}
	if i < 31 {
		// Data[1]
		if f.dlc < 2 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[1] >> (30 - i)) & 1)
	}
	if i < 39 {
		// Data[2]
		if f.dlc < 3 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[2] >> (38 - i)) & 1)
	}
	if i < 47 {
		// Data[3]
		if f.dlc < 4 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[3] >> (46 - i)) & 1)
	}
	if i < 55 {
		// Data[4]
		if f.dlc < 5 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[4] >> (54 - i)) & 1)
	}
	if i < 63 {
		// Data[5]
		if f.dlc < 6 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[5] >> (62 - i)) & 1)
	}
	if i < 71 {
		// Data[6]
		if f.dlc < 7 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[6] >> (70 - i)) & 1)
	}
	if i < 79 {
		// Data[7]
		if f.dlc < 8 {
			panic("can: bit index out of range")
		}
		return uint8((f.data[7] >> (78 - i)) & 1)
	}
	panic("can: bit index out of range")
}

func (f *DataFrame) BitString() string {
	var s string
	for i := 0; i < f.Size(); i++ {
		s += fmt.Sprintf("%b", f.Bit(i))
		if i == 10 || i == 14 {
			s += " "
		}
	}
	return s
}

func dataString(data []uint8) string {
	return fmt.Sprintf("%X", data)
}
