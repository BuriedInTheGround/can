package can

import (
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"
)

const (
	Dominant  uint8 = 0
	Recessive uint8 = 1
)

type Bus struct {
	state atomic.Uint32

	nodes    []*Node
	canRead  []chan struct{}
	canWrite []chan struct{}

	active  bool
	bitTime time.Duration
}

func NewBus(bitrate int) *Bus {
	b := &Bus{bitTime: time.Second / time.Duration(bitrate)}
	b.state.Store(uint32(Recessive))
	return b
}

func (b *Bus) BitTime() time.Duration {
	return b.bitTime
}

func (b *Bus) Attach(node *Node) (int, error) {
	if b.active {
		return 0, fmt.Errorf("bus is active")
	}
	uid := len(b.nodes)
	b.nodes = append(b.nodes, node)
	return uid, nil
}

func (b *Bus) Activate() error {
	if b.active {
		return fmt.Errorf("bus already active")
	}
	b.canRead = make([]chan struct{}, len(b.nodes))
	b.canWrite = make([]chan struct{}, len(b.nodes))
	for i := range b.nodes {
		b.canRead[i] = make(chan struct{}, 1)
		b.canWrite[i] = make(chan struct{}, 1)
	}
	b.active = true
	slog.Info("Bus is active", "bitTime", b.bitTime)
	for _, node := range b.nodes {
		go node.activate()
	}
	for n := 0; ; n++ {
		// reset state
		b.state.Store(uint32(Recessive))

		// write phase
		for i := range b.nodes {
			b.canWrite[i] <- struct{}{}
		}
		time.Sleep(b.bitTime / 2)
		for i := range b.nodes {
			clearChan(b.canWrite[i])
		}

		slog.Debug("Bus write phase end", "state", b.state.Load(), "goroutines", runtime.NumGoroutine(), "n", n)

		// read phase
		for i := range b.nodes {
			b.canRead[i] <- struct{}{}
		}
		time.Sleep(b.bitTime / 2)
		for i := range b.nodes {
			clearChan(b.canRead[i])
		}
	}
}

func (b *Bus) Read(uid int) uint8 {
	if !b.active {
		panic("can: bus is not active")
	}
	<-b.canRead[uid]
	return uint8(b.state.Load())
}

func (b *Bus) Write(uid int, bit uint8) {
	if !b.active {
		panic("can: bus is not active")
	}
	<-b.canWrite[uid]
	b.state.And(uint32(bit))
}

func clearChan(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
