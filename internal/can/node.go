package can

import (
	"fmt"
	"log/slog"
	"sync"
)

type NodeState int

const (
	StateErrorActive NodeState = iota
	StateErrorPassive
	StateBusOff
)

func (s NodeState) String() string {
	switch s {
	case StateErrorActive:
		return "error active"
	case StateErrorPassive:
		return "error passive"
	case StateBusOff:
		return "bus off"
	}
	return "unknown"
}

type Node struct {
	bus *Bus
	uid int

	id    uint16
	tec   uint
	rec   uint
	state NodeState

	busOffFunc func()
	behavior   func()
	queue      []*DataFrame
	mu         sync.Mutex

	lastBitRead            uint8
	lastBitWrite           uint8
	samePolarityReadCount  int
	samePolarityWriteCount int
}

func NewNode(bus *Bus, id uint16) (*Node, error) {
	var err error
	n := &Node{bus: bus, id: id}
	n.uid, err = bus.Attach(n)
	if err != nil {
		return nil, fmt.Errorf("cannot attach to bus: %w", err)
	}
	return n, nil
}

func (n *Node) SetBusOffFunc(busOffFunc func()) {
	n.busOffFunc = busOffFunc
}

func (n *Node) SetBehavior(behavior func()) {
	n.behavior = behavior
}

func (n *Node) Enqueue(data ...uint8) error {
	df, err := NewDataFrame(n.id, data...)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.queue = append(n.queue, df)
	n.mu.Unlock()
	return nil
}

func (n *Node) enqueueFront(df *DataFrame) {
	n.mu.Lock()
	n.queue = append([]*DataFrame{df}, n.queue...)
	n.mu.Unlock()
}

func (n *Node) dequeue() *DataFrame {
	n.mu.Lock()
	if len(n.queue) == 0 {
		n.mu.Unlock()
		return nil
	}
	f := n.queue[0]
	n.queue = n.queue[1:]
	n.mu.Unlock()
	return f
}

type fsmState int

const (
	fsmIntermission fsmState = iota
	fsmSuspendTransmission
	fsmBusIdle
	fsmArbitration
	fsmDLC
	fsmData
	fsmEOF
	fsmError
)

type nodeRole int

const (
	roleReceiver nodeRole = iota
	roleTransmitter
)

func (r nodeRole) String() string {
	switch r {
	case roleReceiver:
		return "receiver"
	case roleTransmitter:
		return "transmitter"
	}
	return "unknown"
}

func (n *Node) activate() {
	if n.behavior != nil {
		ready := make(chan struct{})
		go func() {
			ready <- struct{}{}
			n.behavior()
		}()
		<-ready
	}
	slog.Info("Node is active", "uid", n.uid)
	var (
		previousRole nodeRole
		currentState fsmState
		currentRole  nodeRole
		currentFrame *DataFrame
		receivedDLC  uint8
		sentDLC      uint8
		errorUpdate  func()
	)
	currentState = fsmBusIdle
	for {
		switch currentState {
		case fsmIntermission:
			slog.Debug("Node at fsmIntermission", "uid", n.uid)
			n.resetStuffingCounters()
			currentFrame = nil
			errorUpdate = nil
			for i := 0; i < 3; i++ {
				n.busWrite(Recessive)
				if n.busRead() == Dominant {
					i = -1
					continue
				}
			}
			if n.state == StateErrorPassive && previousRole == roleTransmitter {
				currentState = fsmSuspendTransmission
			} else {
				currentState = fsmBusIdle
			}
		case fsmSuspendTransmission:
			slog.Debug("Node at fsmSuspendTransmission", "uid", n.uid)
			var isImmediateReceiver bool
			for range 8 {
				n.busWrite(Recessive)
				// SOF, handle bit stuffing variables manually
				if n.busRead() == Dominant {
					if n.samePolarityReadCount == 0 || n.lastBitRead == Dominant {
						n.samePolarityReadCount++
					} else {
						n.samePolarityReadCount = 1
					}
					n.lastBitRead = Dominant
					isImmediateReceiver = true
					break
				}
			}
			if isImmediateReceiver {
				currentRole = roleReceiver
				currentState = fsmArbitration
			} else {
				currentState = fsmBusIdle
			}
		case fsmBusIdle:
			slog.Debug("Node at fsmBusIdle", "uid", n.uid)
			currentFrame = n.dequeue()
			if currentFrame != nil {
				slog.Info("Data frame dequeue", "uid", n.uid, "df", currentFrame.BitString())
				currentRole = roleTransmitter
				n.writeBit(Dominant)
				n.readBit()
				currentState = fsmArbitration
			} else if n.readBit() == Dominant {
				currentRole = roleReceiver
				currentState = fsmArbitration
			}
		case fsmArbitration:
			slog.Debug("Node at fsmArbitration", "uid", n.uid, "role", currentRole)
			switch currentRole {
			case roleReceiver:
				for i := 0; i < 11; i++ {
					if !n.maybeReadStuff() {
						errorUpdate = func() {
							n.rec++ // fault confinement rule 1 (stuff error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
					n.readBit()
				}
			case roleTransmitter:
				var i int
				for i = 0; i < 11; i++ {
					if n.maybeWriteStuff() {
						i--
						continue
					}
					if !n.maybeReadStuff() {
						errorUpdate = func() {
							n.tec += 8 // fault confinement rule 3 (stuff error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
					sent := currentFrame.Bit(i)
					n.writeBit(sent)
					read := n.readBit()
					if read != sent {
						slog.Info("Lost arbitration", "uid", n.uid, "i", i)
						currentRole = roleReceiver
						break
					}
				}
				if currentState != fsmError {
					if i == 11 {
						slog.Info("Won arbitration", "uid", n.uid)
					}
					for i = i + 1; i < 11; i++ { // break skips i++
						if !n.maybeReadStuff() {
							errorUpdate = func() {
								n.rec++ // fault confinement rule 1 (stuff error)
								n.updateState()
							}
							currentState = fsmError
							break
						}
						n.readBit()
					}
				}
			}
			if currentState != fsmError {
				currentState = fsmDLC
			}
		case fsmDLC:
			slog.Debug("Node at fsmDLC", "uid", n.uid, "role", currentRole)
			switch currentRole {
			case roleReceiver:
				receivedDLC = 0
				for i := 0; i < 4; i++ {
					if !n.maybeReadStuff() {
						errorUpdate = func() {
							n.rec++ // fault confinement rule 1 (stuff error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
					dlcBit := n.readBit()
					receivedDLC += dlcBit << (3 - i)
				}
				if currentState != fsmError {
					slog.Info("Read DLC", "uid", n.uid, "dlc", receivedDLC)
				}
			case roleTransmitter:
				var i int
				sentDLC = 0
				for i = 11; i < 15; i++ {
					if n.maybeWriteStuff() {
						i--
						continue
					}
					if !n.maybeReadStuff() {
						errorUpdate = func() {
							n.tec += 8 // fault confinement rule 3 (stuff error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
					sent := currentFrame.Bit(i)
					sentDLC += sent << (14 - i)
					n.writeBit(sent)
					read := n.readBit()
					if read != sent {
						errorUpdate = func() {
							n.tec += 8 // fault confinement rule 3 (bit error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
				}
				if i == 15 {
					slog.Info("Sent DLC", "uid", n.uid, "dlc", sentDLC)
				}
			}
			if currentState != fsmError {
				currentState = fsmData
			}
		case fsmData:
			slog.Debug("Node at fsmData", "uid", n.uid, "role", currentRole)
			switch currentRole {
			case roleReceiver:
				data := make([]uint8, receivedDLC)
				for i := range receivedDLC {
					var dataByte uint8
					for j := range 8 {
						if !n.maybeReadStuff() {
							errorUpdate = func() {
								n.rec++ // fault confinement rule 1 (stuff error)
								n.updateState()
							}
							currentState = fsmError
							break
						}
						bit := n.readBit()
						dataByte += bit << (7 - j)
					}
					data[i] = dataByte
				}
				slog.Info("Read data", "uid", n.uid, "data", dataString(data))
			case roleTransmitter:
				var i int
				lastBitNum := int(15 + 8*sentDLC)
				for i = 15; i < lastBitNum; i++ {
					if n.maybeWriteStuff() {
						i--
						continue
					}
					if !n.maybeReadStuff() {
						errorUpdate = func() {
							n.tec += 8 // fault confinement rule 3 (stuff error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
					sent := currentFrame.Bit(i)
					n.writeBit(sent)
					read := n.readBit()
					if read != sent {
						errorUpdate = func() {
							n.tec += 8 // fault confinement rule 3 (bit error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
				}
				if i == lastBitNum {
					n.mu.Lock()
					l := len(n.queue)
					n.mu.Unlock()
					slog.Info("Sent data", "uid", n.uid, "queue_len", l)
				} else {
					n.enqueueFront(currentFrame)
				}
			}
			if currentState != fsmError {
				currentState = fsmEOF
			}
		case fsmEOF:
			slog.Debug("Node at fsmEOF", "uid", n.uid, "role", currentRole)
			switch currentRole {
			case roleReceiver:
				for i := 0; i < 7; i++ {
					if n.busRead() != Recessive {
						errorUpdate = func() {
							n.rec++ // fault confinement rule 1 (form error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
				}
				if currentState != fsmError && n.rec > 0 {
					// fault confinement rule 8 (successful reception)
					if n.rec > 127 {
						n.rec = 126 // arbitrary value between 119 and 127
					} else {
						n.rec--
					}
					n.updateState()
				}
			case roleTransmitter:
				for i := 0; i < 7; i++ {
					n.busWrite(Recessive)
					if n.busRead() != Recessive {
						errorUpdate = func() {
							n.tec += 8 // fault confinement rule 3 (bit error)
							n.updateState()
						}
						currentState = fsmError
						break
					}
				}
				if currentState != fsmError && n.tec > 0 {
					n.tec-- // fault confinement rule 7 (successful transmission)
					n.updateState()
				}
			}
			if currentState != fsmError {
				previousRole = currentRole
				currentState = fsmIntermission
			}
		case fsmError:
			slog.Debug("Node at fsmError", "uid", n.uid, "role", currentRole)
			switch currentRole {
			case roleReceiver:
				switch n.state {
				case StateErrorActive:
					for i := 0; i < 6; i++ {
						n.busWrite(Dominant)
						if n.busRead() == Recessive {
							n.rec += 8 // fault confinement rule 5 (bit error)
							n.updateState()
							// TODO(simone): should the transmission restart?
						}
					}
				case StateErrorPassive:
					isFirstBit := true
					for i := 0; i < 6; i++ {
						n.busWrite(Recessive)
						if n.busRead() == Dominant {
							if isFirstBit {
								n.rec += 8 // fault confinement rule 2 (first bit dominant)
								n.updateState()
								isFirstBit = false
							}
							i = -1
							continue
						}
					}
				}
			case roleTransmitter:
				switch n.state {
				case StateErrorActive:
					for i := 0; i < 6; i++ {
						n.busWrite(Dominant)
						if n.busRead() == Recessive {
							n.tec += 8 // fault confinement rule 4 (bit error)
							n.updateState()
							// TODO(simone): should the transmission restart?
						}
					}
				case StateErrorPassive:
					for i := 0; i < 6; i++ {
						n.busWrite(Recessive)
						if n.busRead() == Dominant {
							i = -1
							continue
						}
					}
				}
			}
			// TODO(simone): implement fault confinement rule 6
			for {
				n.busWrite(Recessive)
				if n.busRead() == Recessive {
					break
				}
			}
			for i := 0; i < 7; i++ {
				n.busWrite(Recessive)
				n.busRead() // needed for synchronization
			}
			if errorUpdate != nil {
				errorUpdate()
			}
			previousRole = currentRole
			currentState = fsmIntermission
		}
	}
}

func (n *Node) updateState() {
	switch n.state {
	case StateErrorActive:
		// fault confinement rule 9
		if n.tec > 127 || n.rec > 127 {
			n.state = StateErrorPassive
			slog.Warn("Node is error passive", "uid", n.uid)
		}
	case StateErrorPassive:
		// fault confinement rule 11
		if n.tec < 128 && n.rec < 128 {
			n.state = StateErrorActive
			slog.Warn("Node is error active", "uid", n.uid)
		}
		// fault confinement rule 10
		if n.tec > 255 {
			n.state = StateBusOff
			slog.Warn("Node is bus off", "uid", n.uid)
		}
	case StateBusOff:
		// TODO(simone): implement fault confinement rule 12
	}
	slog.Info("Node state update", "uid", n.uid, "tec", n.tec, "rec", n.rec, "state", n.state)
	if n.state == StateBusOff && n.busOffFunc != nil {
		n.busOffFunc()
	}
}

func (n *Node) busRead() uint8 {
	return n.bus.Read(n.uid)
}

func (n *Node) busWrite(bit uint8) {
	if n.state == StateBusOff {
		slog.Debug("Attempt to write in bus off", "uid", n.uid, "bit", bit)
		return
	}
	n.bus.Write(n.uid, bit)
}

func (n *Node) readBit() uint8 {
	bit := n.busRead()
	if n.samePolarityReadCount == 0 || n.lastBitRead == bit {
		n.samePolarityReadCount++
	} else {
		n.samePolarityReadCount = 1
	}
	n.lastBitRead = bit
	return bit
}

func (n *Node) writeBit(bit uint8) {
	n.busWrite(bit)
	if n.samePolarityWriteCount == 0 || n.lastBitWrite == bit {
		n.samePolarityWriteCount++
	} else {
		n.samePolarityWriteCount = 1
	}
	n.lastBitWrite = bit
}

func (n *Node) maybeReadStuff() (ok bool) {
	if n.samePolarityReadCount == 5 {
		prev := n.lastBitRead
		bit := n.readBit()
		slog.Debug("Read stuffing", "uid", n.uid, "bit", bit)
		return bit != prev
	}
	return true
}

func (n *Node) maybeWriteStuff() (wrote bool) {
	if n.samePolarityWriteCount == 5 {
		bit := ^n.lastBitWrite & 1
		n.writeBit(bit)
		slog.Debug("Wrote stuffing", "uid", n.uid, "bit", bit)
		return true
	}
	return false
}

func (n *Node) resetStuffingCounters() {
	n.samePolarityReadCount = 0
	n.samePolarityWriteCount = 0
}
